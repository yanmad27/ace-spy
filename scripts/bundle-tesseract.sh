#!/usr/bin/env bash
# Bundles a portable Tesseract + language data into dist/ (run on a Windows runner with bash).
set -euo pipefail
cd "$(dirname "$0")/.."
src="${TESSERACT_DIR:-/c/Program Files/Tesseract-OCR}"
[[ -x "$src/tesseract.exe" ]] || { echo "tesseract.exe not found in $src" >&2; exit 1; }
mkdir -p dist/tesseract dist/tessdata
cp "$src"/*.exe "$src"/*.dll dist/tesseract/
rm -f dist/tesseract/*uninstall*.exe dist/tesseract/winpath.exe
declare -A sha=(
  [vie]=b6b49293d95d0b6dbd8780174627e82c75be957b6f4ed9862155540d6b00bb45
  [eng]=8280aed0782fe27257a68ea10fe7ef324ca0f8d85bd2fd145d1c2b560bcb66ba
)
for lang in vie eng; do
  out="dist/tessdata/$lang.traineddata"
  curl -sSfL -o "$out" "https://github.com/tesseract-ocr/tessdata_best/raw/4.1.0/$lang.traineddata"
  echo "${sha[$lang]}  $out" | sha256sum -c -
done
dist/tesseract/tesseract.exe --tessdata-dir dist/tessdata --list-langs
