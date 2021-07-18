package main

import (
	"context"
	"errors"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type storage interface {
	storeFile(src string, dst string) error
}

type storageConfig struct {
	Type        string   `json:"type"`
	AWSEndpoint string   `json:"aws_endpoint,omitempty"`
	Bucket      string   `json:"bucket,omitempty"`
	Command     []string `json:"command,omitempty"`
}

func createStorage(cfg *storageConfig) (storage, error) {
	if strings.EqualFold(cfg.Type, "s3") {
		c, err := config.LoadDefaultConfig(context.TODO(),
			config.WithSharedCredentialsFiles([]string{"aws/credentials"}),
			config.WithSharedConfigFiles([]string{"aws/config"}),
			config.WithEndpointResolver(aws.EndpointResolverFunc(func(service, region string) (aws.Endpoint, error) {
				if cfg.AWSEndpoint != "" {
					return aws.Endpoint{
						URL:           cfg.AWSEndpoint,
						SigningRegion: region,
					}, nil
				}

				return aws.Endpoint{}, &aws.EndpointNotFoundError{}
			})))
		if err != nil {
			return nil, err
		}

		return &s3Storage{
			client: s3.NewFromConfig(c),
			bucket: cfg.Bucket,
		}, nil
	} else if strings.EqualFold(cfg.Type, "custom") {
		return customStorage(cfg.Command), nil
	}

	return nil, errors.New("Invalid storage type")
}

type customStorage []string

func (s customStorage) storeFile(src string, dst string) error {
	replacer := strings.NewReplacer("${src}", src, "${dst}", dst)
	args := make([]string, len(s)-1)
	for i := range args {
		args[i] = replacer.Replace(s[i+1])
	}

	return exec.Command(s[0], args...).Run()
}

type s3Storage struct {
	client *s3.Client
	bucket string
}

func (s *s3Storage) storeFile(src string, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	if info.IsDir() {
		files, err := ioutil.ReadDir(src)
		if err != nil {
			return err
		}

		for _, fi := range files {
			err = s.storeFile(src+"/"+fi.Name(), dst+"/"+fi.Name())
			if err != nil {
				return err
			}
		}

		return nil
	} else {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = s.client.PutObject(context.TODO(), &s3.PutObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(dst),
			Body:   f,
			ACL:    types.ObjectCannedACLPublicRead,
		})
		return err
	}
}
