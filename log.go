// Package ui regroupe les sorties lisibles de la toolbox.
//
// Regle : aucun secret ne transite jamais par ce logger.
package ui

import (
	"fmt"
	"io"
	"os"
)

// Logger ecrit des messages lisibles sur la sortie standard.
type Logger struct {
	out     io.Writer
	errOut  io.Writer
	verbose bool
	dryRun  bool
}

// New construit un logger.
func New(verbose, dryRun bool) *Logger {
	return &Logger{out: os.Stdout, errOut: os.Stderr, verbose: verbose, dryRun: dryRun}
}

// DryRun indique si l'execution est simulee.
func (l *Logger) DryRun() bool { return l.dryRun }

// Step affiche une etape du plan.
func (l *Logger) Step(n, total int, desc string) {
	mark := " "
	if l.dryRun {
		mark = "~"
	}
	fmt.Fprintf(l.out, "%s [%d/%d] %s\n", mark, n, total, desc)
}

// Info affiche une information de contexte.
func (l *Logger) Info(format string, a ...any) {
	fmt.Fprintf(l.out, "        "+format+"\n", a...)
}

// Warn affiche un avertissement non bloquant.
func (l *Logger) Warn(format string, a ...any) {
	fmt.Fprintf(l.errOut, "  ATTENTION: "+format+"\n", a...)
}

// Debug n'affiche qu'en mode verbeux.
func (l *Logger) Debug(format string, a ...any) {
	if l.verbose {
		fmt.Fprintf(l.out, "        . "+format+"\n", a...)
	}
}
