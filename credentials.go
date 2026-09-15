package ssh

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// CredentialProvider fournit le mot de passe de la cible.
//
// C'est le seul point d'extension a toucher pour automatiser l'authentification
// plus tard (Vault, deja present dans la plateforme). Aucun Deployer ne connait
// la provenance du secret.
type CredentialProvider interface {
	Password(user, host string) (string, error)
}

// Prompt demande le mot de passe au terminal, sans echo, une seule fois par
// execution. Le secret n'est jamais ecrit sur disque ni passe en argument de
// commande, ou il serait visible dans ps.
type Prompt struct {
	cached string
	asked  bool
}

// NewPrompt construit le fournisseur interactif.
func NewPrompt() *Prompt { return &Prompt{} }

// Password implemente CredentialProvider.
func (p *Prompt) Password(user, host string) (string, error) {
	if p.asked {
		return p.cached, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf(
			"mot de passe requis mais l'entree standard n'est pas un terminal ; " +
				"cette version n'accepte pas d'autre source de secret")
	}
	fmt.Fprintf(os.Stderr, "Mot de passe pour %s@%s : ", user, host)
	raw, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("lecture du mot de passe : %w", err)
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("mot de passe vide")
	}
	p.cached = string(raw)
	p.asked = true
	return p.cached, nil
}
