package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"golang.org/x/net/publicsuffix"
)

var (
	totalImages   = 0
	successImages = 0
	failImages    = 0
	extCounts     = make(map[string]int)

	globalClient *http.Client

	goodLog *os.File
	badLog  *os.File
)

func main() {
	// Create a cookie jar for the global client
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		panic(err)
	}

	// Create a transport that uses keep-alive connections
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	// Create a global client that will be reused for all network operations
	globalClient = &http.Client{
		Transport: transport,
		Jar:       jar,
		Timeout:   30 * time.Second,
	}

	// Open log files
	var logErr error
	goodLog, logErr = os.Create("good.txt")
	if logErr != nil {
		panic("Failed to open good.txt: " + logErr.Error())
	}
	defer goodLog.Close()

	badLog, logErr = os.Create("bad.txt")
	if logErr != nil {
		panic("Failed to open bad.txt: " + logErr.Error())
	}
	defer badLog.Close()

	myApp := app.New()
	myWindow := myApp.NewWindow("Google Spreadsheet Image Downloader")

	urlEntry := widget.NewEntry()
	urlEntry.SetPlaceHolder("Enter Google Spreadsheet URL")

	progressBar := widget.NewProgressBar()
	statusLabel := widget.NewLabel("Status: Idle")

	mainImageText := widget.NewMultiLineEntry()
	mainImageText.SetPlaceHolder("New main_image content will appear here")

	imageCacheText := widget.NewMultiLineEntry()
	imageCacheText.SetPlaceHolder("New image_cache content will appear here")

	downloadButton := widget.NewButton("Download Images", func() {
		go func() {
			spreadsheetURL := urlEntry.Text
			if spreadsheetURL == "" {
				showError(myWindow, errors.New("Please enter a URL"))
				return
			}

			statusLabel.SetText("Status: Fetching CSV data...")
			csvURL, err := getCSVURL(spreadsheetURL)
			if err != nil {
				showError(myWindow, err)
				statusLabel.SetText("Status: Idle")
				return
			}

			records, err := fetchCSV(csvURL)
			if err != nil {
				showError(myWindow, err)
				statusLabel.SetText("Status: Idle")
				return
			}

			statusLabel.SetText("Status: Checking existing images...")
			if dirExists("products") {
				dialog.ShowConfirm("Directory Exists",
					`"products" directory already exists. Do you want to delete it and proceed?`,
					func(b bool) {
						if b {
							err := os.RemoveAll("products")
							if err != nil {
								showError(myWindow, fmt.Errorf("Failed to delete 'products' directory: %v", err))
								return
							}
							continueProcessing(records, statusLabel, progressBar, mainImageText, imageCacheText, myWindow)
						} else {
							statusLabel.SetText("Status: Operation Aborted")
							showError(myWindow, fmt.Errorf("'products' directory exists, aborting ..."))
							return
						}
					}, myWindow)
			} else {
				continueProcessing(records, statusLabel, progressBar, mainImageText, imageCacheText, myWindow)
			}
		}()
	})

	content := container.NewVBox(
		urlEntry,
		downloadButton,
		progressBar,
		statusLabel,
		widget.NewLabel("New main_image Data:"),
		mainImageText,
		widget.NewLabel("New image_cache Data:"),
		imageCacheText,
	)

	myWindow.SetContent(content)
	myWindow.Resize(fyne.NewSize(800, 600))
	myWindow.ShowAndRun()
}

func getCSVURL(spreadsheetURL string) (string, error) {
	u, err := url.Parse(spreadsheetURL)
	if err != nil {
		return "", err
	}

	parts := strings.Split(u.Path, "/")
	var spreadsheetID string
	for i, part := range parts {
		if part == "d" && i+1 < len(parts) {
			spreadsheetID = parts[i+1]
			break
		}
	}
	if spreadsheetID == "" {
		return "", errors.New("Invalid Google Spreadsheet URL")
	}

	q := u.Query()
	gid := q.Get("gid")
	if gid == "" {
		if u.Fragment != "" {
			fragParts := strings.Split(u.Fragment, "=")
			if len(fragParts) == 2 && fragParts[0] == "gid" {
				gid = fragParts[1]
			}
		}
		if gid == "" {
			gid = "0"
		}
	}

	csvURL := fmt.Sprintf("https://docs.google.com/spreadsheets/d/%s/export?format=csv&gid=%s", spreadsheetID, gid)

	return csvURL, nil
}

func fetchCSV(csvURL string) ([][]string, error) {
	req, err := http.NewRequest("GET", csvURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := globalClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Failed to fetch CSV data: %s", resp.Status)
	}

	reader := csv.NewReader(resp.Body)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}

	return records, nil
}

func processRecords(records [][]string, progressBar *widget.ProgressBar, statusLabel *widget.Label, mainImageText, imageCacheText *widget.Entry) error {
	if len(records) < 2 {
		return errors.New("No data in CSV")
	}

	headers := records[0]
	headerMap := make(map[string]int)
	for i, h := range headers {
		headerMap[h] = i
	}

	requiredColumns := []string{"main_image", "image_cache", "brand_seo_url"}
	for _, col := range requiredColumns {
		if _, ok := headerMap[col]; !ok {
			return fmt.Errorf("Missing required column: %s", col)
		}
	}

	// Check for either "seo_url" or "seo_url_uk"
	seoURLColumn := ""
	if _, ok := headerMap["seo_url"]; ok {
		seoURLColumn = "seo_url"
	} else if _, ok := headerMap["seo_url_uk"]; ok {
		seoURLColumn = "seo_url_uk"
	} else {
		return fmt.Errorf("Missing required column: seo_url or seo_url_uk")
	}

	totalRows := len(records) - 1
	progressBar.Max = float64(totalRows)
	progressBar.SetValue(0)

	// Pre-allocate slices to keep order
	mainImageData := make([]string, totalRows)
	imageCacheData := make([]string, totalRows)

	var mu sync.Mutex // To synchronize access to UI elements
	for rowIndex, row := range records[1:] {
		// fyne.CurrentApp().SendNotification(&fyne.Notification{
		// 	Title:   "Processing",
		// 	Content: fmt.Sprintf("Processing row %d/%d", rowIndex+1, totalRows),
		// })

		mainImageURL := row[headerMap["main_image"]]
		imageCacheURLs := row[headerMap["image_cache"]]
		brandSEOURL := row[headerMap["brand_seo_url"]]
		seoURL := row[headerMap[seoURLColumn]]

		// Default to old values
		newMainImagePath := row[headerMap["main_image"]]
		newImageCachePath := row[headerMap["image_cache"]]

		// Process main_image if it's a valid URL
		if isValidImageURL(mainImageURL) {
			totalImages++
			statusLabel.SetText(fmt.Sprintf("Status: Downloading main_image (row %d/from %d)", rowIndex+1, totalRows))
			mpath, err := downloadAndSaveImage(mainImageURL, brandSEOURL, seoURL, fmt.Sprintf("m%d", rowIndex))
			if err != nil {
				// Download failed, keep old main_image content
				failImages++
				logFailure(fmt.Sprintf("MAIN IMAGE FAIL (row %d): %s -> %s | %v", rowIndex+2, mainImageURL, mpath, err))
			} else {
				successImages++
				newMainImagePath = mpath
				logSuccess(fmt.Sprintf("MAIN IMAGE OK (row %d): %s -> %s", rowIndex+2, mainImageURL, mpath))
			}
		} else {
			logFailure(fmt.Sprintf("MAIN IMAGE FAIL (row %d): %s is not a valid URL", rowIndex+2, mainImageURL))
		}

		// Process image_cache if it's a valid URL
		if imageCacheURLs != "" {
			statusLabel.SetText(fmt.Sprintf("Status: Downloading image_cache (row %d/from %d)", rowIndex+1, totalRows))
			var urls []string
			if strings.Contains(imageCacheURLs, "|") {
				urls = strings.Split(imageCacheURLs, "|")
			} else if strings.Contains(imageCacheURLs, ",") {
				urls = strings.Split(imageCacheURLs, ",")
			} else {
				urls = []string{imageCacheURLs}
			}

			var downloadedPaths []string

			for i, imgURL := range urls {
				imgURL = strings.TrimSpace(imgURL)
				if imgURL == "" {
					// Empty URL, skip
					continue
				}
				if !isValidImageURL(imgURL) {
					// Not a valid URL, skip this single image
					// Do not break out; just skip this image
					failImages++
					logFailure(fmt.Sprintf("CACHE IMAGE FAIL (row %d): %s is not a valid URL", rowIndex+2, imgURL))
					continue
				}

				totalImages++
				newPath, err := downloadAndSaveImage(imgURL, brandSEOURL, seoURL, fmt.Sprintf("i%d_j%d", rowIndex, i))
				if err != nil {
					// On download failure, just skip this image
					failImages++
					logFailure(fmt.Sprintf("CACHE IMAGE FAIL (row %d): %s -> %s | %v", rowIndex+2, imgURL, newPath, err))
					continue // move on to the next image without reverting
				} else {
					successImages++
					logSuccess(fmt.Sprintf("CACHE IMAGE OK (row %d): %s -> %s", rowIndex+2, imgURL, newPath))
					if newPath != "" {
						downloadedPaths = append(downloadedPaths, newPath)
					}
				}
			}

			// If some images were successfully downloaded, update the path.
			// If none were downloaded successfully, this will be empty.
			newImageCachePath = strings.Join(downloadedPaths, "|")
		}

		// Update progress bar and status label
		mu.Lock()
		mainImageData[rowIndex] = newMainImagePath
		imageCacheData[rowIndex] = newImageCachePath
		progressBar.SetValue(float64(rowIndex + 1))
		mu.Unlock()
	}

	// Update the text boxes with new data
	// Prevent GUI freezing for long textes
	if len(mainImageData) > 500 {
		mainImageText.SetText("Too many products. See result in main_image.txt")
	} else {
		mainImageText.SetText(strings.Join(mainImageData, "\n"))
	}
	if len(imageCacheData) > 500 {
		imageCacheText.SetText("Too many products. See result in image_cache.txt")
	} else {
		imageCacheText.SetText(strings.Join(imageCacheData, "\n"))
	}
	writeDataToFile(mainImageData, "main_image.txt")
	writeDataToFile(imageCacheData, "image_cache.txt")

	return nil
}

func continueProcessing(records [][]string, statusLabel *widget.Label, progressBar *widget.ProgressBar, mainImageText, imageCacheText *widget.Entry, myWindow fyne.Window) {
	statusLabel.SetText("Status: Processing records...")
	err := processRecords(records, progressBar, statusLabel, mainImageText, imageCacheText)
	if err != nil {
		showError(myWindow, err)
		statusLabel.SetText("Status: Idle")
		return
	}

	finalMessage := fmt.Sprintf("Download completed, %d images of %d downloaded. %d Failed.\n", successImages, totalImages, failImages)
	finalMessage += "Extension counts:\n"

	for ext, count := range extCounts {
		finalMessage += fmt.Sprintf("%s: %d Schtuck.\n", ext, count)
	}

	statusLabel.SetText(finalMessage)
	showInfo(myWindow, fmt.Sprintf("Images downloaded,\n %d images of %d downloaded. %d Failed.", successImages, totalImages, failImages))
}

// Check if URL starts with http, https or ftp
func isValidImageURL(imageURL string) bool {
	imageURL = strings.ToLower(imageURL)
	return strings.HasPrefix(imageURL, "http://") || strings.HasPrefix(imageURL, "https://") || strings.HasPrefix(imageURL, "ftp://")
}

// downloadAndSaveImage behaves more like a browser when downloading images
func downloadAndSaveImage(imageURL, brandSEOURL, seoURL, imageType string) (string, error) {
	if imageURL == "" {
		// No URL provided
		return "", nil
	}

	baseDir := "products"
	brandDir := filepath.Join(baseDir, brandSEOURL)
	err := os.MkdirAll(brandDir, os.ModePerm)
	if err != nil {
		return "", err
	}

	ext := filepath.Ext(imageURL)
	if ext == "" || len(ext) > 5 {
		ext = ".jpg"
	}

	extCounts[ext]++

	filename := fmt.Sprintf("%s_%s%s", seoURL, imageType, ext)
	filePath := filepath.Join(brandDir, filename)
	relativePath := filepath.ToSlash(filePath) // For consistent path separators

	if _, err := os.Stat(filePath); err == nil {
		// File already exists, this is considered a success (already downloaded)
		logSuccess(fmt.Sprintf("ALREADY EXISTS: %s", filePath))
		return relativePath, nil
	}

	// Create an HTTP request with custom headers
	req, err := http.NewRequest("GET", imageURL, nil)
	if err != nil {
		return relativePath, err
	}

	// Add browser-like headers to the request
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Referer", "https://www.google.com/") // Simulating a referrer

	// Perform the HTTP request
	resp, err := globalClient.Do(req)
	if err != nil {
		return relativePath, fmt.Errorf("Failed to execute HTTP request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return relativePath, fmt.Errorf("Failed to download image: %s", resp.Status)
	}

	// Save the image to a file
	out, err := os.Create(filePath)
	if err != nil {
		return relativePath, fmt.Errorf("Failed to create file: %v", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return relativePath, fmt.Errorf("Failed to save image to file: %v", err)
	}

	// Check if the content length matches the downloaded file size
	if resp.ContentLength > 0 {
		stat, err := out.Stat()
		if err == nil && stat.Size() != resp.ContentLength {
			return relativePath, fmt.Errorf("File size mismatch: expected %d bytes, got %d bytes", resp.ContentLength, stat.Size())
		}
	}

	return relativePath, nil
}

func showError(win fyne.Window, err error) {
	fyne.CurrentApp().SendNotification(&fyne.Notification{
		Title:   "Error",
		Content: err.Error(),
	})
	dialog.ShowError(err, win)
}

func showInfo(win fyne.Window, message string) {
	fyne.CurrentApp().SendNotification(&fyne.Notification{
		Title:   "Success",
		Content: message,
	})
	dialog.ShowInformation("Success", message, win)
}

func dirExists(dir string) bool {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return false
	}
	return info.IsDir()
}

func writeDataToFile(data []string, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	for _, line := range data {
		_, err := file.WriteString(line + "\n")
		if err != nil {
			return fmt.Errorf("failed to write to file: %w", err)
		}
	}

	return nil
}

// logSuccess writes a success message to good.log
func logSuccess(msg string) {
	if goodLog != nil {
		goodLog.WriteString(msg + "\n")
	}
}

// logFailure writes a failure message to bad.log
func logFailure(msg string) {
	if badLog != nil {
		badLog.WriteString(msg + "\n")
	}
}
