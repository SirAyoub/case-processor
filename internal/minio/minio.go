package minio

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/sirayoub/case-processor/config"
)

type Client struct {
	client *minio.Client
	bucket string
}

func New(cfg *config.Config) (*Client, error) {
	mc, err := minio.New(cfg.MinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinioAccessKey, cfg.MinioSecretKey, ""),
		Secure: cfg.MinioUseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minio client: %w", err)
	}
	return &Client{client: mc, bucket: cfg.MinioBucket}, nil
}

// ListPDFs returns all PDF file names in the bucket
func (c *Client) ListPDFs(ctx context.Context) ([]string, error) {
	var files []string
	objectCh := c.client.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{
		Recursive: true,
	})
	for object := range objectCh {
		if object.Err != nil {
			return nil, fmt.Errorf("list objects: %w", object.Err)
		}
		if strings.HasSuffix(strings.ToLower(object.Key), ".pdf") {
			files = append(files, object.Key)
		}
	}
	return files, nil
}

// DownloadPDF downloads a PDF to a local file path
func (c *Client) DownloadPDF(ctx context.Context, objectName, destPath string) error {
	obj, err := c.client.GetObject(ctx, c.bucket, objectName, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("get object %s: %w", objectName, err)
	}
	defer obj.Close()

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create local file %s: %w", destPath, err)
	}
	defer f.Close()

	if _, err = io.Copy(f, obj); err != nil {
		return fmt.Errorf("copy object to file: %w", err)
	}
	return nil
}
