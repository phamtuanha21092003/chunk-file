package action

import (
	"context"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type RepoS3 struct {
	s3Client *s3.Client

	*s3.PresignClient
}

var repoS3 *RepoS3

func LoadS3Client() {
	repoS3 = newS3Client("admin", "minio@123456", "us-east-1")
}

func newS3Client(accessKey string, secretKey string, s3BucketRegion string) *RepoS3 {
	options := s3.Options{
		Region:      s3BucketRegion,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	}

	client := s3.New(options, func(o *s3.Options) {
		o.Region = s3BucketRegion
		o.UseAccelerate = false
		o.BaseEndpoint = aws.String("http://minio:9000")
		o.UsePathStyle = true
	})

	presignClient := s3.NewPresignClient(client)

	return &RepoS3{
		s3Client:      client,
		PresignClient: presignClient,
	}
}

func (repo *RepoS3) PutObject(bucketName string, objectKey string, lifetimeSecs int64) (*v4.PresignedHTTPRequest, error) {
	request, err := repo.PresignPutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectKey),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = time.Duration(lifetimeSecs * int64(time.Second))
	})
	if err != nil {
		log.Printf("Couldn't get a presigned request to put %v:%v. Here's why: %v\n",
			bucketName, objectKey, err)
	}
	return request, err
}

func GetRepoS3() *RepoS3 {
	if repoS3 == nil {
		LoadS3Client()
	}

	return repoS3
}
