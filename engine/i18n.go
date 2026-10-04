// Traduzione dei messaggi che arrivano all'app (fasi, avvisi, errori, descrizioni).
// La lingua la sceglie l'app con ENCODY_LANG ("en"); senza, o da terminale, resta l'italiano.
// La CLI interattiva (menu e stampe) resta in italiano.
package main

import "os"

var Lang = os.Getenv("ENCODY_LANG")

// T restituisce la traduzione inglese di un testo italiano, se richiesta e disponibile
func T(it string) string {
	if Lang == "en" {
		if en, ok := enStrings[it]; ok {
			return en
		}
	}
	return it
}
