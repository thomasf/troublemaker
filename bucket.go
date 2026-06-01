package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/rs/zerolog"
)

// redactedEnviron returns os.Environ()-style entries with the values of secret
// variables masked, so they never reach public-facing output such as the
// startup "env" log line (which is exposed via /logs). Any variable whose name
// contains SECRET, TOKEN or PASSWORD is redacted — this covers
// BUCKET_SECRET_ACCESS_KEY as well as AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN.
func redactedEnviron(environ []string) []string {
	out := make([]string, len(environ))
	for i, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if ok && (strings.Contains(upper, "SECRET") ||
			strings.Contains(upper, "TOKEN") ||
			strings.Contains(upper, "PASSWORD")) {
			out[i] = name + "=***redacted***"
			continue
		}
		out[i] = kv
	}
	return out
}

// DefaultBucketTimeout caps how long the startup S3 connectivity check may run.
const DefaultBucketTimeout = 10 * time.Second

// SecretString is a string flag value whose contents are redacted when the
// Flags struct is marshalled to JSON (e.g. on /info and /docs), so that the
// bucket secret access key never leaks into responses or logs.
type SecretString string

func (s *SecretString) String() string { return string(*s) }

func (s *SecretString) Set(v string) error {
	*s = SecretString(v)
	return nil
}

func (s SecretString) MarshalJSON() ([]byte, error) {
	if s == "" {
		return []byte(`""`), nil
	}
	return []byte(`"***redacted***"`), nil
}

// Configured reports whether any BUCKET_* setting was provided, which is the
// signal to attempt an S3 connection at startup.
func (f Flags) bucketConfigured() bool {
	return f.BucketRegion != "" ||
		f.BucketEndpoint != "" ||
		f.BucketAccessKeyID != "" ||
		f.BucketSecretAccessKey != "" ||
		f.BucketName != ""
}

// connectBucket builds an aws-sdk-go-v2 S3 client from the BUCKET_* settings
// and verifies connectivity by listing the contents of the bucket root. Any
// error is returned to the caller.
func connectBucket(ctx context.Context, flags Flags, logger zerolog.Logger) error {
	opts := []func(*awsconfig.LoadOptions) error{}
	if flags.BucketRegion != "" {
		opts = append(opts, awsconfig.WithRegion(flags.BucketRegion))
	}
	if flags.BucketAccessKeyID != "" || flags.BucketSecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				flags.BucketAccessKeyID, string(flags.BucketSecretAccessKey), "",
			),
		))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return err
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if flags.BucketEndpoint != "" {
			o.BaseEndpoint = aws.String(flags.BucketEndpoint)
			// Custom/S3-compatible endpoints (MinIO, Ceph, localstack)
			// typically require path-style addressing.
			o.UsePathStyle = true
		}
	})

	logger.Info().
		Str("region", flags.BucketRegion).
		Str("endpoint", flags.BucketEndpoint).
		Str("bucket", flags.BucketName).
		Msg("checking s3 bucket connectivity")

	if flags.BucketName == "" {
		return errors.New("bucket.name (BUCKET_NAME) is required to list the bucket root")
	}

	// List the contents of the bucket root. The "/" delimiter rolls everything
	// below the first level up into CommonPrefixes so we see the top-level
	// "folders" and objects rather than the whole recursive key space.
	out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:    aws.String(flags.BucketName),
		Delimiter: aws.String("/"),
	})
	if err != nil {
		return err
	}

	prefixes := make([]string, 0, len(out.CommonPrefixes))
	for _, p := range out.CommonPrefixes {
		prefixes = append(prefixes, aws.ToString(p.Prefix))
	}
	keys := make([]string, 0, len(out.Contents))
	for _, o := range out.Contents {
		keys = append(keys, aws.ToString(o.Key))
	}

	logger.Info().
		Str("bucket", flags.BucketName).
		Strs("prefixes", prefixes).
		Strs("objects", keys).
		Bool("truncated", aws.ToBool(out.IsTruncated)).
		Msg("listed s3 bucket root")

	return nil
}

// checkBucket runs the startup connectivity check and, if it fails and
// bucket.crash.on.error is set, crashes the process. This lets troublemaker
// simulate a service that cannot start because a required S3 dependency is
// unavailable.
func checkBucket(flags Flags, logger zerolog.Logger) {
	if !flags.bucketConfigured() {
		return
	}

	timeout := flags.BucketTimeout
	if timeout <= 0 {
		timeout = DefaultBucketTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := connectBucket(ctx, flags, logger); err != nil {
		logger.Error().Err(err).Msg("s3 bucket connection failed")
		if flags.BucketCrashOnError {
			logger.Fatal().Err(err).Msg("crashing on startup: s3 bucket unreachable and bucket.crash.on.error is set")
		}
		return
	}

	logger.Info().Msg("s3 bucket connection ok")
}
