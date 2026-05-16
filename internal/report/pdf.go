package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/jung-kurt/gofpdf"

	"github.com/Provincia-di-Pescara/e-conomato/internal/models"
)

// toLatin1 converte una stringa UTF-8 in Latin-1 (ISO-8859-1) per i font core di gofpdf.
// I caratteri italiani accentati sono tutti nel range U+00C0–U+00FF, quindi sono coperti.
func toLatin1(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r <= 0xFF {
			b.WriteByte(byte(r))
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

func newPDF() *gofpdf.Fpdf {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(20, 20, 20)
	pdf.SetAutoPageBreak(true, 15)
	return pdf
}

// WriteContoGiudizialePDF genera il PDF del Conto Giudiziale conforme a Modello 21.
func WriteContoGiudizialePDF(w io.Writer, s models.SezioneContoGiudiziale, brandName string) error {
	pdf := newPDF()
	pdf.AddPage()

	// Header
	pdf.SetFont("Helvetica", "B", 10)
	pdf.CellFormat(0, 6, toLatin1(brandName), "", 1, "C", false, 0, "")
	pdf.SetFont("Helvetica", "B", 13)
	pdf.CellFormat(0, 8, "CONTO GIUDIZIALE DEL FONDO ECONOMALE", "", 1, "C", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	pdf.CellFormat(0, 6, "D.P.R. 194/1996 - Modello 21", "", 1, "C", false, 0, "")
	pdf.SetFont("Helvetica", "B", 11)
	pdf.CellFormat(0, 8, fmt.Sprintf("Anno %d", s.Anno), "", 1, "C", false, 0, "")
	pdf.Ln(6)

	// Larghezze colonne
	wLabel := 140.0
	wImporto := 30.0

	// Intestazione tabella
	pdf.SetFillColor(230, 230, 230)
	pdf.SetFont("Helvetica", "B", 9)
	pdf.CellFormat(wLabel, 7, "Voce", "1", 0, "L", true, 0, "")
	pdf.CellFormat(wImporto, 7, "Importo (EUR)", "1", 1, "R", true, 0, "")

	row := func(label string, importo float64, bold bool) {
		style := ""
		if bold {
			style = "B"
		}
		pdf.SetFont("Helvetica", style, 9)
		pdf.SetFillColor(255, 255, 255)
		pdf.CellFormat(wLabel, 6, "  "+toLatin1(label), "1", 0, "L", false, 0, "")
		pdf.CellFormat(wImporto, 6, fmtFloat(importo), "1", 1, "R", false, 0, "")
	}
	section := func(title string) {
		pdf.SetFillColor(240, 240, 240)
		pdf.SetFont("Helvetica", "B", 9)
		pdf.CellFormat(wLabel+wImporto, 6, "  "+toLatin1(title), "1", 1, "L", true, 0, "")
	}

	section("A - Entrate")
	row("Fondo iniziale", s.FondoIniziale, false)
	row("Reintegri", s.TotaleReintegri, false)
	section("B - Uscite")
	row("Totale spese", s.TotaleSpese, false)
	section("C - Saldo")
	row("Saldo finale", s.SaldoFinale, true)
	row("Restituito in tesoreria", s.RestituitoInTesoreria, false)

	// Firma (A4 = 210mm, margine destro 20mm → SetX a 210-20-60=130)
	pdf.Ln(15)
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetX(130)
	pdf.CellFormat(60, 5, "Firma Economo:", "", 1, "L", false, 0, "")
	pdf.SetX(130)
	pdf.CellFormat(60, 5, "___________________________", "", 1, "L", false, 0, "")

	return pdf.Output(w)
}

// WriteRichiestaReintegroPDF genera il PDF della richiesta di reintegro.
func WriteRichiestaReintegroPDF(w io.Writer, r models.Reintegro, righe []models.RigaReintegro, brandName string) error {
	pdf := newPDF()
	pdf.AddPage()

	// Header
	pdf.SetFont("Helvetica", "B", 10)
	pdf.CellFormat(0, 6, toLatin1(brandName), "", 1, "C", false, 0, "")
	pdf.SetFont("Helvetica", "B", 13)
	pdf.CellFormat(0, 8, fmt.Sprintf("RICHIESTA DI REINTEGRO R-%d/%d", r.Anno, r.Numero), "", 1, "C", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	pdf.CellFormat(0, 6, fmt.Sprintf("Data: %s   Importo: EUR %s", fmtDate(r.DataRichiesta), fmtFloat(r.ImportoTotale)), "", 1, "C", false, 0, "")
	pdf.Ln(6)

	// Raggruppa per capitolo
	type gruppo struct {
		peg         string
		descrizione string
		righe       []models.RigaReintegro
	}
	var gruppi []gruppo
	var cur *gruppo
	for _, riga := range righe {
		if cur == nil || cur.peg != riga.CodicePEG {
			gruppi = append(gruppi, gruppo{peg: riga.CodicePEG, descrizione: riga.DescrizionePEG})
			cur = &gruppi[len(gruppi)-1]
		}
		cur.righe = append(cur.righe, riga)
	}

	// Larghezze colonne (A4 = 210mm, margini 20+20 → spazio utile 170mm)
	const pw = 170.0
	wPratica := 20.0
	wFornitore := 45.0
	wData := 25.0
	wEstremi := 30.0
	wImporto := 22.0
	wOggetto := pw - wPratica - wFornitore - wData - wEstremi - wImporto

	colHeader := func() {
		pdf.SetFillColor(230, 230, 230)
		pdf.SetFont("Helvetica", "B", 7)
		pdf.CellFormat(wPratica, 5, "Pratica", "1", 0, "C", true, 0, "")
		pdf.CellFormat(wFornitore, 5, "Fornitore", "1", 0, "L", true, 0, "")
		pdf.CellFormat(wOggetto, 5, "Oggetto", "1", 0, "L", true, 0, "")
		pdf.CellFormat(wData, 5, "Data doc.", "1", 0, "C", true, 0, "")
		pdf.CellFormat(wEstremi, 5, "Estremi", "1", 0, "L", true, 0, "")
		pdf.CellFormat(wImporto, 5, "Importo", "1", 1, "R", true, 0, "")
	}

	for _, g := range gruppi {
		// Intestazione capitolo
		pdf.SetFillColor(245, 245, 245)
		pdf.SetFont("Helvetica", "B", 8)
		pdf.CellFormat(0, 6, fmt.Sprintf("  %s - %s", toLatin1(g.peg), toLatin1(g.descrizione)), "1", 1, "L", true, 0, "")
		colHeader()

		var subtotale float64
		for _, riga := range g.righe {
			pdf.SetFont("Helvetica", "", 7)
			pdf.SetFillColor(255, 255, 255)
			pdf.CellFormat(wPratica, 5, toLatin1(riga.NumeroPratica), "1", 0, "C", false, 0, "")
			pdf.CellFormat(wFornitore, 5, toLatin1(truncate(riga.Fornitore, 30)), "1", 0, "L", false, 0, "")
			pdf.CellFormat(wOggetto, 5, toLatin1(truncate(riga.Oggetto, 40)), "1", 0, "L", false, 0, "")
			pdf.CellFormat(wData, 5, fmtDate(riga.DataDocumento), "1", 0, "C", false, 0, "")
			pdf.CellFormat(wEstremi, 5, toLatin1(truncate(riga.EstremiDocumento, 20)), "1", 0, "L", false, 0, "")
			pdf.CellFormat(wImporto, 5, fmtFloat(riga.Importo), "1", 1, "R", false, 0, "")
			subtotale += riga.Importo
		}
		// Subtotale
		pdf.SetFont("Helvetica", "B", 7)
		pdf.SetFillColor(245, 245, 245)
		pdf.CellFormat(wPratica+wFornitore+wOggetto+wData+wEstremi, 5, fmt.Sprintf("  Subtotale %s", toLatin1(g.peg)), "1", 0, "R", true, 0, "")
		pdf.CellFormat(wImporto, 5, fmtFloat(subtotale), "1", 1, "R", true, 0, "")
		pdf.Ln(2)
	}

	// Totale generale
	pdf.SetFont("Helvetica", "B", 9)
	pdf.CellFormat(pw-wImporto, 6, "TOTALE RICHIESTA", "T", 0, "R", false, 0, "")
	pdf.CellFormat(wImporto, 6, "EUR "+fmtFloat(r.ImportoTotale), "T", 1, "R", false, 0, "")

	return pdf.Output(w)
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}
