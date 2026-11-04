# OCImageDownloader

**OCImageDownloader** is a desktop GUI application built with Go and the [Fyne](https://fyne.io/) toolkit. It allows users to download images from URLs specified in a Google Spreadsheet, organizing them into a structured directory format. The application fetches image URLs from a CSV export of the spreadsheet, downloads them, and produces local file paths to copy back into the spreadsheet or export as a complete CSV in CLI mode.

## CLI (v1.2.0)

```bash
ocimagedownloader -i in.csv -o out.csv --main-fallback
ocimagedownloader -u 'https://docs.google.com/spreadsheets/d/ID/edit#gid=0' -o out.csv -d ./work
ocimagedownloader --verify out.csv -d ./work
```

With arguments, the application runs CLI processing without opening a window.
Without arguments, the normal build opens the GUI. The standalone CLI release
has no GUI dependencies and prints usage guidance when invoked without arguments.

Input supports CSV, XLSX and ODS (first worksheet). Required columns are
`main_image`, `image_cache`, `brand_seo_url`, and `seo_url` or `seo_url_uk`.
Brand/product identifiers must be nonempty file names without path separators.
Google Sheets must permit unauthenticated CSV export.

`-o` writes the entire table, replacing only the two image columns. CSV preserves
UTF-8 BOM, comma/semicolon delimiter, line endings and original cell quoting;
XLSX/ODS exports use comma-separated UTF-8 CSV. Other cell values and row order
are preserved. Additional image URLs may be separated by `|` or commas; output
paths use `|`. Output/report parent directories must already exist.

Options:

| Option | Behavior |
| --- | --- |
| `-d DIR` | Image/log directory; defaults to the input file's directory, or cwd for URLs |
| `--resume` | Default: retain existing nonempty files matching remote Content-Length; unknown lengths are downloaded again |
| `--clean` | Delete `DIR/products` before processing |
| `--main-fallback` | Promote the first successful cache image if main is unavailable |
| `--workers 4` | Concurrent product rows, with stable output order |
| `--retries 3` | Additional attempts for network errors, HTTP 429, 5xx and 6xx; waits 2/5/10 seconds |
| `--timeout 30` | Timeout per HTTP request, in seconds |
| `--check-format` | Report URL extension/content mismatches; files always receive detected image extensions |
| `--report FILE` | JSON report location; default `DIR/report.json` |
| `--verify FILE` | Check that referenced files exist and are nonempty, without downloading |
| `--version` | Print application version |

A missing main image becomes an empty cell, never the original URL. Failed cache
images are omitted. Exit status is 0 when every output row has a main image
(including fallback), 1 when a main image is missing, and 2 for input or output
errors. Cache failures alone do not cause exit status 1. Verification returns
1 for missing/empty paths and 2 for invalid input.

Downloads use `.part` files and rename after successful completion. TLS
certificates are validated. Images are saved without transcoding: WebP-to-JPEG
conversion is not implemented; use `--check-format` to report mismatches.
Progress goes to stderr and a final summary to stdout. `good.txt`, `bad.txt`,
`main_image.txt`, `image_cache.txt`, and `report.json` are written inside `-d`.
Reports include spreadsheet row numbers (header is row 1), HTTP statuses, main
status (`ok`, `fail`, `fallback`), and cache success/failure counts.

Build the standalone Linux amd64 CLI:

```bash
make build-cli
# Or:
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags headless -o ocimagedownloader .
go test -race -tags headless ./...
```

## Features

- **User-Friendly Interface**: Simple and intuitive GUI to input Google Spreadsheet URLs and manage downloads.
- **Directory Management**: Automatically checks for existing directories and prompts users to delete them before proceeding.
- **Progress Tracking**: Real-time progress bar and status updates during the download process.
- **Resizable Text Areas**: Scrollable and resizable text areas to display new `main_image` and `image_cache` paths.
- **Browser-Like Downloads**: Mimics browser behavior by setting appropriate HTTP headers and handling cookies.

## Screenshots

![Start Program](screenshots/main_interface1.png)
*Starting of OCImageDownloader with existing image dir*

![Main Interface](screenshots/main_interface2.png)
*Main interface of OCImageDownloader showing URL input, progress bar, and text areas.*

## Installation

### Prerequisites

- **Go**: Ensure you have Go installed. You can download it from [golang.org](https://golang.org/dl/).
- **Fyne Toolkit**: Install Fyne v2 by running:

  ```bash
  go get fyne.io/fyne/v2
  ```

### Clone the Repository

```bash
git clone https://github.com/vitovt/OCImageDownloader.git
cd OCImageDownloader
```

### Build the Application

```bash
go build -o OCImageDownloader .
```

### Run the Application

After building, run the application with:

```bash
./OCImageDownloader
```

On Windows, the executable will be `OCImageDownloader.exe`.

Alternatively, you can run it directly without building:

```bash
go run .
```

## Usage

1. **Enter Spreadsheet URL**:
   - Open the application and paste your Google Spreadsheet URL into the input field labeled "Enter Google Spreadsheet URL".

2. **Download Images**:
   - Click the "Download Images" button to start the download process.

3. **Handling Existing Directory**:
   - If a `products` directory already exists in the application's directory, a confirmation dialog will appear.
   - **Yes**: Deletes the existing `products` directory and proceeds with downloading.
   - **No**: Aborts the download operation.

4. **Monitor Progress**:
   - The progress bar and status label will update in real-time, indicating the current status of the download process.

5. **View New Data**:
   - After the download completes, the new `main_image` and `image_cache` data (file paths starting with `products/...`) will appear in the respective text areas.
   - These text areas are scrollable and resizable. You can select and copy the text to paste back into your spreadsheet.

## Example Workflow

### Spreadsheet Structure

| main_image                   | image_cache                                     | brand_seo_url | seo_url     |
|------------------------------|-------------------------------------------------|---------------|-------------|
| http://example.com/image1.jpg | http://example.com/image2.jpg\|http://example.com/image3.jpg | BrandA        | product-123 |

### After Processing

- **Images Saved**:
  - `products/BrandA/product-123_main_image_0.jpg`
  - `products/BrandA/product-123_image_cache_0_0.jpg`
  - `products/BrandA/product-123_image_cache_0_1.jpg`

- **New Data Displayed**:
  - **main_image Text Box**:
    ```
    products/BrandA/product-123_main_image_0.jpg
    ```
  - **image_cache Text Box**:
    ```
    products/BrandA/product-123_image_cache_0_0.jpg|products/BrandA/product-123_image_cache_0_1.jpg
    ```

### Updating Spreadsheet

1. **main_image**:
   - Copy the content from the `main_image` text box.
   - Paste it into the `main_image` column in your spreadsheet.

2. **image_cache**:
   - Similarly, copy the `image_cache` data.
   - Paste it into the `image_cache` column.

## Contributing

Contributions are welcome! Please follow these steps to contribute:

1. **Fork the Repository**:
   - Click the "Fork" button at the top-right corner of this page.

2. **Clone Your Fork**:
   ```bash
   git clone https://github.com/vitovt/OCImageDownloader.git
   cd OCImageDownloader
   ```

3. **Create a New Branch**:
   ```bash
   git checkout -b feature/YourFeatureName
   ```

4. **Make Your Changes**:
   - Implement your feature or fix.

5. **Commit Your Changes**:
   ```bash
   git commit -m "Add feature: YourFeatureName"
   ```

6. **Push to Your Fork**:
   ```bash
   git push origin feature/YourFeatureName
   ```

7. **Create a Pull Request**:
   - Navigate to the original repository and create a pull request from your fork.

## License

This project is licensed under the [MIT License](LICENSE).

## Acknowledgements

- Built with the [Fyne](https://fyne.io/) toolkit.
- Inspired by the need to automate image downloads from Google Spreadsheets.

## Contact

For any inquiries or support, don't hesitate to contact me.

