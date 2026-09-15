// Package ssh porte l'unique connexion ouverte pendant une execution.
//
// Le mot de passe etant impose sur les cibles, ouvrir une connexion par
// operation obligerait a le redemander ou a le stocker. Une seule connexion est
// donc etablie, puis reutilisee pour les commandes distantes et pour les
// transferts de fichiers via sftp sur le meme canal.
package ssh

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Client encapsule la connexion SSH et le canal sftp.
type Client struct {
	conn *gossh.Client
	sftp *sftp.Client
	home string
	addr string
}

// Options parametre l'ouverture de connexion.
type Options struct {
	Addr       string
	User       string
	KnownHosts string
	Timeout    time.Duration
}

// Dial ouvre la connexion et resout le HOME distant.
//
// Les deux methodes d'authentification par mot de passe sont proposees :
// beaucoup de serveurs configures en PasswordAuthentication repondent en
// realite en keyboard-interactive via PAM, et n'accepter que password produit
// un echec d'authentification difficile a diagnostiquer.
func Dial(opt Options, cp CredentialProvider) (*Client, error) {
	host := opt.Addr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}

	pw, err := cp.Password(opt.User, host)
	if err != nil {
		return nil, err
	}

	hostKeyCallback, err := hostKeyChecker(opt.KnownHosts)
	if err != nil {
		return nil, err
	}

	if opt.Timeout == 0 {
		opt.Timeout = 20 * time.Second
	}

	cfg := &gossh.ClientConfig{
		User: opt.User,
		Auth: []gossh.AuthMethod{
			gossh.Password(pw),
			gossh.KeyboardInteractive(
				func(name, instruction string, questions []string, echos []bool) ([]string, error) {
					answers := make([]string, len(questions))
					for i := range questions {
						answers[i] = pw
					}
					return answers, nil
				}),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         opt.Timeout,
	}

	conn, err := gossh.Dial("tcp", opt.Addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("connexion SSH vers %s : %w", opt.Addr, err)
	}

	sc, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ouverture du canal sftp : %w", err)
	}

	c := &Client{conn: conn, sftp: sc, addr: opt.Addr}

	// Le HOME distant est resolu une fois : les chemins de configuration
	// contiennent $HOME, qui ne doit surtout pas etre developpe localement.
	home, err := c.Run("printf %s \"$HOME\"")
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("resolution du HOME distant : %w", err)
	}
	c.home = strings.TrimSpace(home)
	if c.home == "" {
		c.Close()
		return nil, fmt.Errorf("HOME distant vide sur %s", opt.Addr)
	}
	return c, nil
}

func hostKeyChecker(path string) (gossh.HostKeyCallback, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".ssh", "known_hosts")
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf(
			"known_hosts inutilisable (%s) : %w ; "+
				"connectez-vous une fois en ssh pour enregistrer l'empreinte de la VM", path, err)
	}
	return cb, nil
}

// Close ferme le canal sftp puis la connexion.
func (c *Client) Close() error {
	if c.sftp != nil {
		c.sftp.Close()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Home renvoie le HOME resolu sur la VM.
func (c *Client) Home() string { return c.home }

// Expand remplace $HOME et ${HOME} par le HOME distant.
func (c *Client) Expand(path string) string {
	path = strings.ReplaceAll(path, "${HOME}", c.home)
	return strings.ReplaceAll(path, "$HOME", c.home)
}

// Run execute une commande et renvoie sa sortie standard.
// La sortie d'erreur est jointe au message en cas d'echec.
func (c *Client) Run(cmd string) (string, error) {
	sess, err := c.conn.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	if err := sess.Run(cmd); err != nil {
		return stdout.String(), fmt.Errorf("commande distante en echec (%s) : %w : %s",
			firstWords(cmd), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// RunOK execute une commande et n'indique que son succes, sans la traiter
// comme une erreur. Utile pour les tests d'existence.
func (c *Client) RunOK(cmd string) bool {
	_, err := c.Run(cmd)
	return err == nil
}

// ReadFile lit un fichier distant via sftp.
func (c *Client) ReadFile(path string) ([]byte, error) {
	f, err := c.sftp.Open(c.Expand(path))
	if err != nil {
		return nil, fmt.Errorf("lecture distante de %s : %w", path, err)
	}
	defer f.Close()
	return io.ReadAll(f)
}

// Stat renvoie les informations d'un chemin distant.
func (c *Client) Stat(path string) (os.FileInfo, error) {
	return c.sftp.Stat(c.Expand(path))
}

// Exists indique si un chemin distant existe.
func (c *Client) Exists(path string) bool {
	_, err := c.sftp.Stat(c.Expand(path))
	return err == nil
}

// ListDir liste les entrees d'un repertoire distant.
func (c *Client) ListDir(path string) ([]os.FileInfo, error) {
	return c.sftp.ReadDir(c.Expand(path))
}

// MkdirAll cree un repertoire distant et ses parents.
func (c *Client) MkdirAll(path string) error {
	return c.sftp.MkdirAll(c.Expand(path))
}

// Upload transfere un fichier local vers la VM.
func (c *Client) Upload(localPath, remotePath string) error {
	src, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer src.Close()

	remotePath = c.Expand(remotePath)
	if dir := filepath.Dir(remotePath); dir != "." {
		if err := c.sftp.MkdirAll(dir); err != nil {
			return fmt.Errorf("creation de %s sur la VM : %w", dir, err)
		}
	}

	dst, err := c.sftp.Create(remotePath)
	if err != nil {
		return fmt.Errorf("creation de %s sur la VM : %w", remotePath, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return fmt.Errorf("transfert vers %s : %w", remotePath, err)
	}
	return dst.Close()
}

// WriteFile ecrit un contenu court dans un fichier distant.
func (c *Client) WriteFile(remotePath string, content []byte) error {
	remotePath = c.Expand(remotePath)
	if dir := filepath.Dir(remotePath); dir != "." {
		if err := c.sftp.MkdirAll(dir); err != nil {
			return err
		}
	}
	f, err := c.sftp.Create(remotePath)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Remove supprime un fichier distant.
func (c *Client) Remove(path string) error { return c.sftp.Remove(c.Expand(path)) }

// Rename renomme un chemin distant.
func (c *Client) Rename(oldPath, newPath string) error {
	return c.sftp.Rename(c.Expand(oldPath), c.Expand(newPath))
}

// Quote protege une chaine pour l'inserer dans une commande shell distante.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstWords(cmd string) string {
	f := strings.Fields(cmd)
	if len(f) > 4 {
		f = f[:4]
	}
	return strings.Join(f, " ")
}
