package blob

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// Client accède au bucket du dashboard.
type Client struct {
	minio      *minio.Client
	bucket     string
	presignTTL time.Duration
	Keys       Keys
}

// Open construit le client et vérifie que le bucket est accessible.
//
// La vérification n'est pas superflue : le client minio-go n'ouvre aucune
// connexion à la construction. Sans elle, un identifiant erroné ou un bucket
// absent passerait le démarrage et se manifesterait à la première publication
// d'artefact — c'est-à-dire à la fin d'un pipeline, après que le travail coûteux
// a été fait.
func Open(ctx context.Context, cfg config.StorageConfig) (*Client, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		// La région n'est pas décorative : elle entre dans le calcul de la
		// signature SigV4. Une région erronée produit un refus dont le message
		// ne mentionne pas la région. L'instance cible utilise "NUO-Region".
		Region: cfg.Region,
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("client MinIO pour %q: %w", cfg.Endpoint, err)
	}

	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	exists, err := client.BucketExists(checkCtx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("accès au bucket %q sur %q: %w", cfg.Bucket, cfg.Endpoint, err)
	}
	if !exists {
		return nil, fmt.Errorf("bucket %q absent sur %q", cfg.Bucket, cfg.Endpoint)
	}

	return &Client{
		minio:      client,
		bucket:     cfg.Bucket,
		presignTTL: cfg.PresignTTL,
	}, nil
}

// Ping alimente la sonde /readyz.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.minio.BucketExists(ctx, c.bucket)
	return err
}

// PresignPut produit une URL d'écriture à usage unique pour une clé donnée.
//
// C'est le mécanisme central de la publication : le pipeline reçoit cette URL en
// paramètre et écrit avec un simple `curl -T`, sans détenir le moindre
// identifiant MinIO et sans choisir où l'artefact atterrit.
//
// L'URL est signée avant que l'artefact existe, donc sans connaître son
// empreinte : minio-go émet `UNSIGNED-PAYLOAD`, que l'instance cible accepte.
// Sans cette tolérance, tout le dispositif tomberait.
//
// La valeur retournée est une capacité d'écriture : elle ne doit jamais être
// journalisée ni exposée en dehors du paramètre de PipelineRun qui la porte.
func (c *Client) PresignPut(ctx context.Context, key string, ttl time.Duration) (*url.URL, error) {
	if ttl <= 0 {
		ttl = c.presignTTL
	}
	if key == "" {
		return nil, fmt.Errorf("clé vide")
	}

	presigned, err := c.minio.PresignedPutObject(ctx, c.bucket, key, ttl)
	if err != nil {
		return nil, fmt.Errorf("signature de l'URL d'écriture pour %q: %w", key, err)
	}
	return presigned, nil
}

// Put écrit un objet. Sert aux copies faites par le dashboard lui-même — un
// artefact rattaché à une release, un log scellé — là où les pipelines passent
// par une URL pré-signée.
func (c *Client) Put(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error {
	_, err := c.minio.PutObject(ctx, c.bucket, key, reader, size,
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("écriture de %q: %w", key, err)
	}
	return nil
}

// Get ouvre un objet en lecture. L'appelant referme.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	object, err := c.minio.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("lecture de %q: %w", key, err)
	}
	// GetObject est paresseux : l'erreur d'objet absent ne survient qu'à la
	// première lecture. Un Stat immédiat la remonte à l'appelant plutôt qu'à
	// l'endroit du code qui consomme le flux.
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, fmt.Errorf("lecture de %q: %w", key, err)
	}
	return object, nil
}

// GetRange lit une plage d'octets.
//
// Indispensable pour les logs : un pipeline d'intégration en produit des dizaines
// de mégaoctets, et l'interface n'en affiche que la fin avant de remonter à la
// demande. Charger l'objet entier pour en montrer les cinquante dernières lignes
// serait ruineux.
func (c *Client) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	options := minio.GetObjectOptions{}
	if err := options.SetRange(offset, offset+length-1); err != nil {
		return nil, fmt.Errorf("plage invalide pour %q: %w", key, err)
	}

	object, err := c.minio.GetObject(ctx, c.bucket, key, options)
	if err != nil {
		return nil, fmt.Errorf("lecture partielle de %q: %w", key, err)
	}
	return object, nil
}

// ObjectInfo décrit un objet listé.
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Stat rend les métadonnées d'un objet, sans son contenu.
//
// Sert à connaître la taille avant une lecture par plage : afficher la fin d'un
// journal de cinquante mégaoctets suppose de savoir où elle commence.
func (c *Client) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := c.minio.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("métadonnées de %q: %w", key, err)
	}
	return ObjectInfo{Key: info.Key, Size: info.Size, LastModified: info.LastModified}, nil
}

// List énumère les objets sous un préfixe.
func (c *Client) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var objects []ObjectInfo

	for object := range c.minio.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if object.Err != nil {
			return nil, fmt.Errorf("listing de %q: %w", prefix, object.Err)
		}
		objects = append(objects, ObjectInfo{
			Key:          object.Key,
			Size:         object.Size,
			LastModified: object.LastModified,
		})
	}
	return objects, nil
}

// Remove supprime un objet.
func (c *Client) Remove(ctx context.Context, key string) error {
	if err := c.minio.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("suppression de %q: %w", key, err)
	}
	return nil
}

// Bucket rend le nom du bucket, pour le diagnostic.
func (c *Client) Bucket() string { return c.bucket }
