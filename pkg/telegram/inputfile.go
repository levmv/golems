package telegram

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// InputFile represents any type of file payload (ID, URL, or physical Upload via Multipart).
type InputFile struct {
	StringValue string
	Filename    string
	Reader      io.Reader
}

// NeedsUpload returns true if this file requires multipart/form-data.
func (f InputFile) NeedsUpload() bool {
	return f.Reader != nil
}

func FileFromID(fileID string) InputFile {
	return InputFile{StringValue: fileID}
}

func FileFromURL(url string) InputFile {
	return InputFile{StringValue: url}
}

func FileFromBytes(filename string, data []byte) InputFile {
	return InputFile{
		Filename: filename,
		Reader:   bytes.NewReader(data),
	}
}

func FileFromReader(filename string, r io.Reader) InputFile {
	return InputFile{
		Filename: filename,
		Reader:   r,
	}
}

func FileFromDisk(path string) (InputFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return InputFile{}, err
	}
	return InputFile{
		Filename: filepath.Base(path),
		Reader:   f,
	}, nil
}

// MarshalJSON allows InputFile to be automatically serialized properly when part of JSON payloads.
func (f InputFile) MarshalJSON() ([]byte, error) {
	if f.NeedsUpload() {
		return []byte(`"attach://` + f.Filename + `"`), nil
	}
	return json.Marshal(f.StringValue)
}
