//go:build !headless

package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	fdialog "fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	sdialog "github.com/sqweek/dialog"
	"golang.org/x/net/publicsuffix"
)

var (
	totalImages   = 0
	successImages = 0
	failImages    = 0
	extCounts     = make(map[string]int)

	goodLog *os.File
	badLog  *os.File
)

func resetCounters() {
	totalImages = 0
	successImages = 0
	failImages = 0
	extCounts = make(map[string]int)
}

func runGUI() {
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

	myApp := app.New()
	myWindow := myApp.NewWindow("Spreadsheet Image Downloader (URL or Local)")

	urlEntry := widget.NewEntry()
	urlEntry.SetPlaceHolder("Enter Google Spreadsheet URL")

	progressBar := widget.NewProgressBar()
	statusLabel := widget.NewLabel("Status: Idle")

	mainImageText := widget.NewMultiLineEntry()
	mainImageText.SetPlaceHolder("New main_image content will appear here")

	imageCacheText := widget.NewMultiLineEntry()
	imageCacheText.SetPlaceHolder("New image_cache content will appear here")

	downloadButton := widget.NewButton("Download Images from Google Sheet URL", func() {
		go func() {
			resetCounters()
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

			handleProductsDirAndProcess(records, statusLabel, progressBar, mainImageText, imageCacheText, myWindow)
		}()
	})

	processLocalButton := widget.NewButton("Process Local Table (CSV/XLSX/ODS)", func() {
		go func() {
			resetCounters()

			// Native OS file dialog
			path, err := sdialog.
				File().
				Filter("Spreadsheets", "csv", "xlsx", "ods").
				Title("Select local table (CSV, XLSX, or ODS)").
				Load()
			if err != nil {
				// user cancelled or backend not available
				// Only show error if it's not a typical cancel
				if !strings.Contains(strings.ToLower(err.Error()), "cancel") {
					showError(myWindow, fmt.Errorf("failed to open native file dialog: %v", err))
				}
				return
			}
			if strings.TrimSpace(path) == "" {
				return
			}

			statusLabel.SetText("Status: Loading local table...")
			records, lerr := loadLocalTable(path)
			if lerr != nil {
				showError(myWindow, lerr)
				statusLabel.SetText("Status: Idle")
				return
			}

			handleProductsDirAndProcess(records, statusLabel, progressBar, mainImageText, imageCacheText, myWindow)
		}()
	})

	content := container.NewVBox(
		urlEntry,
		container.NewHBox(downloadButton, processLocalButton),
		progressBar,
		statusLabel,
		widget.NewLabel("New main_image Data:"),
		mainImageText,
		widget.NewLabel("New image_cache Data:"),
		imageCacheText,
	)

	myWindow.SetContent(content)
	myWindow.Resize(fyne.NewSize(900, 650))
	myWindow.ShowAndRun()
}

func handleProductsDirAndProcess(records [][]string, statusLabel *widget.Label, progressBar *widget.ProgressBar, mainImageText, imageCacheText *widget.Entry, myWindow fyne.Window) {
	if goodLog != nil {
		goodLog.Close()
	}
	if badLog != nil {
		badLog.Close()
	}
	goodLog, _ = os.Create("good.txt")
	badLog, _ = os.Create("bad.txt")
	statusLabel.SetText("Status: Checking existing images...")
	if dirExists("products") {
		fdialog.ShowConfirm("Directory Exists",
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
}

/* ========================= Processing ========================= */

func processRecords(records [][]string, progressBar *widget.ProgressBar, statusLabel *widget.Label, mainImageText, imageCacheText *widget.Entry) error {
	if len(records) < 2 {
		return errors.New("No data in table (need header + at least 1 row)")
	}

	headers := records[0]
	headerMap := make(map[string]int)
	for i, h := range headers {
		headerMap[strings.TrimSpace(h)] = i
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
		mainImageURL := safeIndex(row, headerMap["main_image"])
		imageCacheURLs := safeIndex(row, headerMap["image_cache"])
		brandSEOURL := safeIndex(row, headerMap["brand_seo_url"])
		seoURL := safeIndex(row, headerMap[seoURLColumn])

		// Default to old values
		newMainImagePath := mainImageURL
		newImageCachePath := imageCacheURLs

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
		} else if strings.TrimSpace(mainImageURL) != "" {
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

func safeIndex(row []string, idx int) string {
	if idx >= 0 && idx < len(row) {
		return row[idx]
	}
	return ""
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
	d := downloader{client: globalClient, workdir: ".", retries: 3, pause: time.Sleep}
	path, _, err := d.download(imageURL, brandSEOURL, seoURL, imageType)
	if err == nil {
		extCounts[filepath.Ext(path)]++
	}
	return path, err
}

func showError(win fyne.Window, err error) {
	fyne.CurrentApp().SendNotification(&fyne.Notification{
		Title:   "Error",
		Content: err.Error(),
	})
	fdialog.ShowError(err, win)
}

func showInfo(win fyne.Window, message string) {
	fyne.CurrentApp().SendNotification(&fyne.Notification{
		Title:   "Success",
		Content: message,
	})
	fdialog.ShowInformation("Success", message, win)
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
