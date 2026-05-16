package report

import (
	"archive/zip"
	"io"
)

// WriteAllegatiZip scrive uno ZIP contenente i file passati come mappa nome→blob.
func WriteAllegatiZip(w io.Writer, files map[string][]byte) error {
	zw := zip.NewWriter(w)
	for name, data := range files {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			return err
		}
	}
	return zw.Close()
}
