package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
)

func PrettyPrint(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%+v", v) // fallback
	}
	return string(b)
}

// GetAWSClientFromEC2IMDS retrieves AWS config from EC2 IMDS,
// ignoring any local AWS config (e.g. ~/.aws) and ENV variables.
//
// This ensures that we always assume RunsOn instance profile IAM role, regardless of what happens in other GHA actions/steps.
func GetAWSClientFromEC2IMDS(context context.Context) (*aws.Config, error) {
	provider := ec2rolecreds.New(func(o *ec2rolecreds.Options) {
		o.Client = imds.New(imds.Options{})
	})

	cfg, err := config.LoadDefaultConfig(context, config.WithRegion(os.Getenv("RUNS_ON_AWS_REGION")), config.WithCredentialsProvider(aws.NewCredentialsCache(provider)))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &cfg, nil
}
