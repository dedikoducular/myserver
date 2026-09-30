package files

import (
	"path"
	"strings"
)

// A fixed table is used instead of the system MIME database so results do
// not depend on what is installed on the host.
var mimeTypes = map[string]string{
	".txt": "text/plain", ".log": "text/plain", ".md": "text/markdown", ".csv": "text/csv",
	".json": "application/json", ".xml": "application/xml", ".yaml": "application/yaml", ".yml": "application/yaml",
	".toml": "application/toml", ".ini": "text/plain", ".conf": "text/plain", ".cfg": "text/plain", ".env": "text/plain",
	".html": "text/html", ".htm": "text/html", ".css": "text/css", ".js": "text/javascript", ".ts": "text/typescript",
	".sh": "text/x-shellscript", ".py": "text/x-python", ".go": "text/x-go", ".c": "text/x-c", ".h": "text/x-c",
	".cpp": "text/x-c++", ".java": "text/x-java", ".rs": "text/x-rust", ".php": "text/x-php", ".sql": "application/sql",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".bmp": "image/bmp", ".ico": "image/x-icon", ".avif": "image/avif", ".svg": "image/svg+xml",
	".heic": "image/heic", ".tif": "image/tiff", ".tiff": "image/tiff",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".mkv": "video/x-matroska",
	".avi": "video/x-msvideo", ".mov": "video/quicktime",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".flac": "audio/flac",
	".m4a": "audio/mp4", ".aac": "audio/aac", ".opus": "audio/opus",
	".pdf": "application/pdf", ".doc": "application/msword", ".xls": "application/vnd.ms-excel",
	".ppt":  "application/vnd.ms-powerpoint",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".odt":  "application/vnd.oasis.opendocument.text", ".ods": "application/vnd.oasis.opendocument.spreadsheet",
	".epub": "application/epub+zip",
	".zip":  "application/zip", ".tar": "application/x-tar", ".gz": "application/gzip", ".tgz": "application/gzip",
	".bz2": "application/x-bzip2", ".xz": "application/x-xz", ".7z": "application/x-7z-compressed",
	".rar": "application/vnd.rar", ".zst": "application/zstd",
	".iso": "application/x-iso9660-image", ".img": "application/octet-stream",
	".deb": "application/vnd.debian.binary-package", ".rpm": "application/x-rpm",
	".ttf": "font/ttf", ".otf": "font/otf", ".woff": "font/woff", ".woff2": "font/woff2",
}

func mimeByName(name string) string {
	if t, ok := mimeTypes[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	return "application/octet-stream"
}

// previewTypes are the only types ever served inline. They cannot carry
// script that a browser would run: no HTML, no SVG, no XML, no PDF.
var previewTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".bmp": "image/bmp", ".avif": "image/avif", ".ico": "image/x-icon",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".flac": "audio/flac",
	".m4a": "audio/mp4", ".opus": "audio/opus",
}

func previewType(name string) (string, bool) {
	t, ok := previewTypes[strings.ToLower(path.Ext(name))]
	return t, ok
}

// archiveKind reports how a file name would be extracted: "zip", "tar",
// "tgz" or "" when unsupported.
func archiveKind(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".zip"):
		return "zip"
	case strings.HasSuffix(l, ".tar.gz"), strings.HasSuffix(l, ".tgz"):
		return "tgz"
	case strings.HasSuffix(l, ".tar"):
		return "tar"
	}
	return ""
}
