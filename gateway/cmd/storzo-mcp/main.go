// Command storzo-mcp is a stdio MCP server: a thin S3 client scoped by a
// gateway-issued credential. Scope is enforced server-side by the gateway;
// this process performs no scope checks.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultMaxBytes is the default cap for get_object inline text (1 MiB).
const DefaultMaxBytes = 1048576

// DefaultMaxKeys is the default page size for list_objects.
const DefaultMaxKeys = 100

// MaxMaxKeys is the hard ceiling for list_objects max_keys.
const MaxMaxKeys = 1000

// Config holds the process configuration, from env only.
type Config struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Bucket          string
	Prefix          string
	Mode            string // "ro" or "rw"
}

// loadConfigFromEnv reads configuration using getenv. It returns an error
// naming the missing variables. It never includes secret values in errors.
func loadConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{
		Endpoint:        getenv("STORZO_ENDPOINT"),
		AccessKeyID:     getenv("STORZO_ACCESS_KEY_ID"),
		SecretAccessKey: getenv("STORZO_SECRET_ACCESS_KEY"),
		SessionToken:    getenv("STORZO_SESSION_TOKEN"),
		Bucket:          getenv("STORZO_BUCKET"),
		Prefix:          getenv("STORZO_PREFIX"),
		Mode:            getenv("STORZO_MODE"),
	}
	var missing []string
	if cfg.Endpoint == "" {
		missing = append(missing, "STORZO_ENDPOINT")
	}
	if cfg.AccessKeyID == "" {
		missing = append(missing, "STORZO_ACCESS_KEY_ID")
	}
	if cfg.SecretAccessKey == "" {
		missing = append(missing, "STORZO_SECRET_ACCESS_KEY")
	}
	if cfg.SessionToken == "" {
		missing = append(missing, "STORZO_SESSION_TOKEN")
	}
	if cfg.Bucket == "" {
		missing = append(missing, "STORZO_BUCKET")
	}
	if cfg.Prefix == "" {
		missing = append(missing, "STORZO_PREFIX")
	}
	if cfg.Mode == "" {
		missing = append(missing, "STORZO_MODE")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	if cfg.Mode != "ro" && cfg.Mode != "rw" {
		return Config{}, fmt.Errorf("invalid STORZO_MODE %q: want ro or rw", cfg.Mode)
	}
	return cfg, nil
}

// newS3Client builds a path-style S3 client using the scoped credential,
// including the session token.
func newS3Client(cfg Config) *s3.Client {
	awsCfg := aws.Config{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
		BaseEndpoint: aws.String(cfg.Endpoint),
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})
}

// listObjectsArgs are the arguments for the list_objects tool.
type listObjectsArgs struct {
	Prefix  string `json:"prefix,omitempty" jsonschema:"optional key prefix, defaults to the scope prefix"`
	MaxKeys int32  `json:"max_keys,omitempty" jsonschema:"max keys to return, 1-1000"`
}

// getObjectArgs are the arguments for the get_object tool.
type getObjectArgs struct {
	Key      string `json:"key" jsonschema:"object key to read"`
	MaxBytes int64  `json:"max_bytes,omitempty" jsonschema:"max inline bytes, defaults to 1048576"`
}

// putObjectArgs are the arguments for the put_object tool.
type putObjectArgs struct {
	Key  string `json:"key" jsonschema:"object key to write"`
	Text string `json:"text" jsonschema:"text content to store"`
}

// deleteObjectArgs are the arguments for the delete_object tool.
type deleteObjectArgs struct {
	Key string `json:"key" jsonschema:"object key to delete"`
}

// app wires configuration to an S3 client for tool handlers.
type app struct {
	cfg Config
	s3  *s3.Client
}

func (a *app) listObjects(ctx context.Context, req *mcp.CallToolRequest, args listObjectsArgs) (*mcp.CallToolResult, any, error) {
	prefix := args.Prefix
	if prefix == "" {
		prefix = a.cfg.Prefix
	}
	maxKeys := args.MaxKeys
	if maxKeys == 0 {
		maxKeys = DefaultMaxKeys
	}
	if maxKeys < 0 || maxKeys > MaxMaxKeys {
		return nil, nil, fmt.Errorf("max_keys out of range: want 1-%d", MaxMaxKeys)
	}
	out, err := a.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(a.cfg.Bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(maxKeys),
	})
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	for i, obj := range out.Contents {
		if i > 0 {
			b.WriteString("\n")
		}
		key := ""
		if obj.Key != nil {
			key = *obj.Key
		}
		var size int64
		if obj.Size != nil {
			size = *obj.Size
		}
		fmt.Fprintf(&b, "%s (%d bytes)", key, size)
	}
	if b.Len() == 0 {
		b.WriteString("(no objects)")
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
	}, nil, nil
}

func (a *app) getObject(ctx context.Context, req *mcp.CallToolRequest, args getObjectArgs) (*mcp.CallToolResult, any, error) {
	if args.Key == "" {
		return nil, nil, fmt.Errorf("key is required")
	}
	maxBytes := args.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxBytes < 0 {
		return nil, nil, fmt.Errorf("max_bytes must be >= 0")
	}
	out, err := a.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(a.cfg.Bucket),
		Key:    aws.String(args.Key),
	})
	if err != nil {
		return nil, nil, err
	}
	defer out.Body.Close()
	limited := io.LimitReader(out.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, err
	}
	meta := func() string {
		ct := ""
		if out.ContentType != nil {
			ct = *out.ContentType
		}
		cl := int64(-1)
		if out.ContentLength != nil {
			cl = *out.ContentLength
		}
		return fmt.Sprintf("key=%q content_length=%d content_type=%q max_bytes=%d", args.Key, cl, ct, maxBytes)
	}
	if int64(len(data)) > maxBytes {
		return nil, nil, fmt.Errorf("too_large: %s", meta())
	}
	if !utf8.Valid(data) {
		return nil, nil, fmt.Errorf("too_large: binary content not inlined; %s", meta())
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}

func (a *app) putObject(ctx context.Context, req *mcp.CallToolRequest, args putObjectArgs) (*mcp.CallToolResult, any, error) {
	if args.Key == "" {
		return nil, nil, fmt.Errorf("key is required")
	}
	_, err := a.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(a.cfg.Bucket),
		Key:    aws.String(args.Key),
		Body:   strings.NewReader(args.Text),
	})
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("wrote %s (%d bytes)", args.Key, len(args.Text))}},
	}, nil, nil
}

func (a *app) deleteObject(ctx context.Context, req *mcp.CallToolRequest, args deleteObjectArgs) (*mcp.CallToolResult, any, error) {
	if args.Key == "" {
		return nil, nil, fmt.Errorf("key is required")
	}
	_, err := a.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(a.cfg.Bucket),
		Key:    aws.String(args.Key),
	})
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("deleted %s", args.Key)}},
	}, nil, nil
}

// buildServer registers list_objects and get_object always, plus put_object
// and delete_object only when mode is rw.
func buildServer(a *app) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "storzo-mcp", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "list_objects", Description: "List objects under a prefix (defaults to the scope prefix)"}, a.listObjects)
	mcp.AddTool(server, &mcp.Tool{Name: "get_object", Description: "Read an object as text; larger or binary content returns a too_large error with metadata"}, a.getObject)
	if a.cfg.Mode == "rw" {
		mcp.AddTool(server, &mcp.Tool{Name: "put_object", Description: "Write text to an object"}, a.putObject)
		mcp.AddTool(server, &mcp.Tool{Name: "delete_object", Description: "Delete an object"}, a.deleteObject)
	}
	return server
}

func main() {
	cfg, err := loadConfigFromEnv(os.Getenv)
	if err != nil {
		// Never print secret values; loadConfigFromEnv only names variables.
		log.Fatalf("storzo-mcp: %v", err)
	}
	a := &app{cfg: cfg, s3: newS3Client(cfg)}
	server := buildServer(a)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("storzo-mcp: %v", err)
	}
}
