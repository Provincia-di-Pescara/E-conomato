package report

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Provincia-di-Pescara/e-conomato/internal/models"
)

// WriteGiornaleCassaCSV scrive il giornale di cassa nel formato CSV italiano (BOM, ';', ',').
func WriteGiornaleCassaCSV(w io.Writer, righe []models.RigaGiornaleCassa) error {
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "data;tipo;pratica;reintegro;descrizione;entrata;uscita;saldo\r\n"); err != nil {
		return err
	}
	for _, riga := range righe {
		if _, err := fmt.Fprintf(w, "%s;%s;%s;%s;%s;%s;%s;%s\r\n",
			riga.Data.Format("02/01/2006"),
			csvEsc(riga.Tipo),
			csvEsc(riga.NumeroPratica),
			csvEsc(riga.NumeroReintegro),
			csvEsc(riga.Descrizione),
			fmtFloat(riga.ImportoEntrata),
			fmtFloat(riga.ImportoUscita),
			fmtFloat(riga.SaldoProgressivo),
		); err != nil {
			return err
		}
	}
	return nil
}

// WriteRichiestaReintegroCSV scrive la richiesta di reintegro nel formato CSV italiano.
func WriteRichiestaReintegroCSV(w io.Writer, r models.Reintegro, righe []models.RigaReintegro) error {
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}
	header := fmt.Sprintf("Reintegro R-%d/%d;;;;;;;\r\nAnno %d;;;;;;;\r\n", r.Anno, r.Numero, r.Anno)
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "capitolo;pratica;fornitore;oggetto;data_documento;estremi;importo\r\n"); err != nil {
		return err
	}
	for _, riga := range righe {
		if _, err := fmt.Fprintf(w, "%s;%s;%s;%s;%s;%s;%s\r\n",
			csvEsc(riga.CodicePEG),
			csvEsc(riga.NumeroPratica),
			csvEsc(riga.Fornitore),
			csvEsc(riga.Oggetto),
			fmtDate(riga.DataDocumento),
			csvEsc(riga.EstremiDocumento),
			fmtFloat(riga.Importo),
		); err != nil {
			return err
		}
	}
	totaleLine := fmt.Sprintf(";;;;;;;%s\r\n", fmtFloat(r.ImportoTotale))
	_, err := io.WriteString(w, totaleLine)
	return err
}

// WriteContoGiudizialeCSV scrive il conto giudiziale (Modello 21) nel formato CSV italiano.
func WriteContoGiudizialeCSV(w io.Writer, s models.SezioneContoGiudiziale) error {
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}
	rows := []struct{ voce, importo string }{
		{"Anno", strconv.Itoa(s.Anno)},
		{"A - Entrate", ""},
		{"Fondo iniziale", fmtFloat(s.FondoIniziale)},
		{"Reintegri", fmtFloat(s.TotaleReintegri)},
		{"B - Uscite", ""},
		{"Totale spese", fmtFloat(s.TotaleSpese)},
		{"C - Saldo", ""},
		{"Saldo finale", fmtFloat(s.SaldoFinale)},
		{"Restituito in tesoreria", fmtFloat(s.RestituitoInTesoreria)},
	}
	if _, err := io.WriteString(w, "voce;importo\r\n"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(w, "%s;%s\r\n", csvEsc(row.voce), row.importo); err != nil {
			return err
		}
	}
	return nil
}

func csvEsc(s string) string {
	if !strings.ContainsAny(s, ";\"\r\n") {
		return s
	}
	return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
}

func fmtFloat(f float64) string {
	return strings.Replace(strconv.FormatFloat(f, 'f', 2, 64), ".", ",", 1)
}

func fmtDate(t time.Time) string {
	return t.Format("02/01/2006")
}
