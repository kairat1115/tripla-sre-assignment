package s3

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
)

type bucketProperties struct {
	Region     string `json:"aws-region"`
	ACL        string `json:"acl"`
	BucketName string `json:"bucket-name"`
}

func (p bucketProperties) Validate() error {
	switch {
	case p.Region == "":
		return fmt.Errorf("missing required property: aws-region")
	case p.ACL == "":
		return fmt.Errorf("missing required property: acl")
	case p.BucketName == "":
		return fmt.Errorf("missing required property: bucket-name")
	default:
		return nil
	}
}

type bucketRequest struct {
	Payload struct {
		Properties bucketProperties `json:"properties"`
	} `json:"payload"`
}

type bucketResponse struct {
	OutputPath string `json:"output_path"`
}

type bucketGenerator struct {
	props bucketProperties
}

func (g *bucketGenerator) Provider() string     { return "aws" }
func (g *bucketGenerator) TemplateName() string { return "s3/bucket.tf.tmpl" }
func (g *bucketGenerator) StoragePath() string  { return "s3/" + g.props.BucketName }
func (g *bucketGenerator) TemplateData() any {
	return struct {
		Properties map[string]string
	}{
		Properties: map[string]string{
			"aws-region":  g.props.Region,
			"acl":         g.props.ACL,
			"bucket-name": g.props.BucketName,
		},
	}
}

type BucketHandler struct {
	svc    handler.Terraform
	logger *zap.Logger
}

func NewBucketHandler(svc handler.Terraform, logger *zap.Logger) *BucketHandler {
	return &BucketHandler{svc: svc, logger: logger}
}

func (h *BucketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	base := []zap.Field{
		zap.String("method", r.Method),
		zap.String("path", r.URL.Path),
		zap.String("remote_addr", r.RemoteAddr),
	}

	var req bucketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Info(err.Error(), append(base,
			zap.Int("status", http.StatusBadRequest),
			zap.Int64("duration_ms", time.Since(start).Milliseconds()),
		)...)
		handler.Respond(w, handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
		return
	}

	p := req.Payload.Properties
	if err := p.Validate(); err != nil {
		msg := err.Error()
		h.logger.Info(msg, append(base,
			zap.Int("status", http.StatusUnprocessableEntity),
			zap.String("aws-region", p.Region),
			zap.String("acl", p.ACL),
			zap.String("bucket-name", p.BucketName),
			zap.Int64("duration_ms", time.Since(start).Milliseconds()),
		)...)
		handler.Respond(w, handler.Result{Code: http.StatusUnprocessableEntity, Msg: msg, Err: err})
		return
	}

	gen := &bucketGenerator{props: p}
	outputPath, err := h.svc.Generate(gen)
	if err != nil {
		h.logger.Error("generation failed", append(base,
			zap.Int("status", http.StatusInternalServerError),
			zap.String("aws-region", p.Region),
			zap.String("acl", p.ACL),
			zap.String("bucket-name", p.BucketName),
			zap.String("error", err.Error()),
			zap.Int64("duration_ms", time.Since(start).Milliseconds()),
		)...)
		handler.Respond(w, handler.Result{Code: http.StatusInternalServerError, Msg: "generation failed", Err: err})
		return
	}

	h.logger.Info("request handled", append(base,
		zap.Int("status", http.StatusCreated),
		zap.String("aws-region", p.Region),
		zap.String("acl", p.ACL),
		zap.String("bucket-name", p.BucketName),
		zap.String("output_path", outputPath),
		zap.Int64("duration_ms", time.Since(start).Milliseconds()),
	)...)
	handler.Respond(w, handler.Result{Code: http.StatusCreated, Data: bucketResponse{OutputPath: outputPath}})
}

var _ service.Generator = (*bucketGenerator)(nil)
