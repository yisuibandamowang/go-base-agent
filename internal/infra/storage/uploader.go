package storage

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"

	"go-base-agent/internal/framework/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Uploader 上传二进制对象并返回可访问 URL。
type Uploader interface {
	Upload(ctx context.Context, key string, data []byte, contentType string) (string, error)
}

type s3API interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// RustFSUploader 将对象上传到 S3 兼容存储。
type RustFSUploader struct {
	client  s3API
	baseURL string
	bucket  string
}

// NewRustFSUploader 创建 RustFSUploader。
func NewRustFSUploader(ctx context.Context, cfg config.RustFSConfig, bucket string) (*RustFSUploader, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, fmt.Errorf("rustfs url is empty")
	}
	if strings.TrimSpace(cfg.AccessKeyID) == "" {
		return nil, fmt.Errorf("rustfs access key is empty")
	}
	if strings.TrimSpace(cfg.SecretAccessKey) == "" {
		return nil, fmt.Errorf("rustfs secret key is empty")
	}
	if strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("rustfs bucket is empty")
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load rustfs config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(strings.TrimRight(cfg.URL, "/"))
		o.UsePathStyle = true
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	return &RustFSUploader{
		client:  client,
		baseURL: strings.TrimRight(cfg.URL, "/"),
		bucket:  bucket,
	}, nil
}

// Upload 上传对象并返回公开 URL。
func (u *RustFSUploader) Upload(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	if u == nil || u.client == nil {
		return "", fmt.Errorf("uploader is nil")
	}
	key = strings.TrimLeft(strings.TrimSpace(key), "/")
	if key == "" {
		return "", fmt.Errorf("upload key is empty")
	}
	if len(data) == 0 {
		return "", fmt.Errorf("upload data is empty")
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	_, err := u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(u.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("put object %s/%s: %w", u.bucket, key, err)
	}
	return strings.TrimRight(u.baseURL, "/") + "/" + path.Join(u.bucket, key), nil
}
