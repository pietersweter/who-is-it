package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/rs/zerolog/log"
	"gopkg.in/validator.v1"

	"github.com/google/uuid"
)

const (
	tableRef  = "Table"
	bucketRef = "Bucket"
	regionRef = "Region"
)

// ImageUploadRequest is a JSON representation of raw request from the client
type ImageUploadRequest struct {
	ImageBase64 string `json:"imageBase64" validate:"nonzero"`
	FileName    string `json:"fileName" validate:"nonzero"`
	Extension   string `json:"extension" validate:"nonzero,regexp=^(jpg|png)$"`
}

// BodyResponse is used to geneerate JSON response for the client
type BodyResponse struct {
	URL string `json:"url"`
}

// UploadHandler is executed for POST to the /celeb endpoint
// it uploads an image to s3 and returns a public URL to it
func UploadHandler(ctx context.Context, reqRaw events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	sess := session.Must(session.NewSession())
	dyna := dynamodb.New(sess)
	uploader := s3manager.NewUploader(sess)

	var reqJSON ImageUploadRequest
	err := json.Unmarshal([]byte(reqRaw.Body), &reqJSON)
	if err != nil {
		log.Error().Err(err).Msgf("error unmarshalling request")
		return events.APIGatewayProxyResponse{Body: "error unmarshalling request", StatusCode: http.StatusBadRequest}, nil
	}

	valid, errs := validator.Validate(reqJSON)
	if !valid {
		for _, err := range errs {
			for _, errMsg := range err {
				log.Error().Err(errMsg).Msgf("validation request failure: %v", errMsg)
			}
		}
		return events.APIGatewayProxyResponse{Body: "validation request body failure\n", StatusCode: http.StatusBadRequest}, nil
	}

	decoded, err := base64.StdEncoding.DecodeString(reqJSON.ImageBase64)
	if err != nil {
		log.Error().Err(err).Msgf("error decoding image base 64: %s", err.Error())
		return events.APIGatewayProxyResponse{Body: "error decoding image base 64\n", StatusCode: http.StatusInternalServerError}, nil
	}

	bucket := os.Getenv(bucketRef)
	uid := uuid.New().String()
	key := getImageNameWithExtension(uid, reqJSON.Extension)
	_, err = uploader.Upload(&s3manager.UploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(decoded),
		ACL:    aws.String("public-read"),
	})
	if err != nil {
		log.Error().Err(err).Msgf("issue uploading to s3: %s", err.Error())
		return events.APIGatewayProxyResponse{Body: "unable to upload to s3\n", StatusCode: http.StatusBadRequest}, nil
	}
	log.Info().Msgf("uploaded image to s3 with key: %s", key)

	table := os.Getenv(tableRef)
	newItem := &dynamodb.PutItemInput{
		Item: map[string]*dynamodb.AttributeValue{
			"ID": {
				S: aws.String(uid),
			},
			"fileName": {
				S: aws.String(reqJSON.FileName),
			},
			"url": {
				S: aws.String(getImagePublicURL(key)),
			},
		},
		TableName: aws.String(table),
	}

	_, err = dyna.PutItem(newItem)
	if err != nil {
		handleDynamoDBError(err)
	}
	log.Info().Msgf("uploaded image to dynamodb with key: %s", uid)

	res := BodyResponse{
		URL: getImagePublicURL(key),
	}
	resRaw, err := json.Marshal(&res)
	if err != nil {
		log.Error().Err(err).Msgf("error marshalling response")
		return events.APIGatewayProxyResponse{Body: err.Error(), StatusCode: http.StatusInternalServerError}, nil
	}

	return events.APIGatewayProxyResponse{Body: string(resRaw), StatusCode: http.StatusOK}, nil
}

func main() {
	lambda.Start(UploadHandler)
}

func getImagePublicURL(key string) string {
	bucketName := os.Getenv(bucketRef)
	region := os.Getenv(regionRef)
	url := fmt.Sprintf(
		"http://%s.s3-%s.amazonaws.com/%s",
		bucketName,
		region,
		key,
	)
	log.Info().Msgf("generated public url: %s", url)
	return url
}

func getImageNameWithExtension(key string, ext string) string {
	name := fmt.Sprintf(
		"%s.%s",
		key, ext,
	)
	log.Info().Msgf("generated image name with extension: %s", name)
	return name
}

func handleDynamoDBError(err error) {
	aerr, ok := err.(awserr.Error)
	if ok {
		switch aerr.Code() {
		case dynamodb.ErrCodeConditionalCheckFailedException:
			log.Error().Err(err).Msgf("%v, %v", dynamodb.ErrCodeConditionalCheckFailedException, aerr.Error())
		case dynamodb.ErrCodeProvisionedThroughputExceededException:
			log.Error().Err(err).Msgf("%v, %v", dynamodb.ErrCodeProvisionedThroughputExceededException, aerr.Error())
		case dynamodb.ErrCodeResourceNotFoundException:
			log.Error().Err(err).Msgf("%v, %v", dynamodb.ErrCodeResourceNotFoundException, aerr.Error())
		case dynamodb.ErrCodeItemCollectionSizeLimitExceededException:
			log.Error().Err(err).Msgf("%v, %v", dynamodb.ErrCodeItemCollectionSizeLimitExceededException, aerr.Error())
		case dynamodb.ErrCodeTransactionConflictException:
			log.Error().Err(err).Msgf("%v, %v", dynamodb.ErrCodeTransactionConflictException, aerr.Error())
		case dynamodb.ErrCodeRequestLimitExceeded:
			log.Error().Err(err).Msgf("%v, %v", dynamodb.ErrCodeRequestLimitExceeded, aerr.Error())
		case dynamodb.ErrCodeInternalServerError:
			log.Error().Err(err).Msgf("%v, %v", dynamodb.ErrCodeInternalServerError, aerr.Error())
		default:
			log.Error().Err(err).Msgf(aerr.Error())
		}
	} else {
		log.Error().Err(err).Msgf(err.Error())
	}
}
