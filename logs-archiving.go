package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go/service/s3"
)

const workspace = string(os.PathSeparator) + "tmp" + string(os.PathSeparator) + "workspace"
const timeout = "10s"

var (
	bucket      string
	environment string
	target      string

	startDate time.Time
	endDate   time.Time

	cwService *cloudwatchlogs.CloudWatchLogs
	s3Service *s3.S3
)

func init() {
	flag.StringVar(&bucket, "bucket", os.Getenv("BUCKET_NAME"), "The S3 bucket name where logs will be archived.")
	flag.StringVar(&environment, "environment", os.Getenv("ENVIRONMENT_NAME"), "The environment name from where logs have been generated.")
	flag.StringVar(&target, "target", os.Getenv("TARGET_DATE"), "The day on which the logs must be archived.")

	sess := session.Must(session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable,
	}))
	cwService = cloudwatchlogs.New(sess)
	s3Service = s3.New(sess)
}

func main() {
	lambda.Start(LambdaHandler)
}

// LambdaHandler handles the archiving process called by AWS Lambda.
func LambdaHandler() {
	log.Println("Start of the logs archiving process.")
	loadFlagValues()

	streamList, err := cwService.DescribeLogStreams(&cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(environment),
	})
	check(err)

	prepareWorkspace()

	var wg sync.WaitGroup
	for _, logStream := range streamList.LogStreams {
		// Avoid long-running processes by skipping files which contain access logs.
		if strings.Contains(*logStream.LogStreamName, "access") {
			continue
		}

		wg.Add(1)
		go func(logStream *cloudwatchlogs.LogStream) {
			defer wg.Done()
			downloadLogs(logStream)
		}(logStream)
	}
	wg.Wait()

	archive, err := os.Create(workspace + string(os.PathSeparator) + startDate.Format("2006-01-02") + ".tar.gz")
	check(err)
	defer archive.Close()

	archiveLogs(archive)
	uploadArchive(archive)
}

// loadFlagValues loads and checks whether all flag values are valid.
func loadFlagValues() {
	flag.Parse()

	if len(bucket) == 0 {
		panic(errors.New("a valid S3 bucket must be provided"))
	}

	if len(environment) == 0 {
		panic(errors.New("a valid environment must be provided"))
	}

	if len(target) > 0 {
		r, _ := regexp.Compile(`\d{4}-\d{2}-\d{2}`)
		if r.MatchString(target) != true {
			panic(errors.New("a valid target date must be provided (YYYY-MM-DD)"))
		}

		startDate, _ = time.Parse("2006-01-02", target)
	} else {
		yesterday := time.Now().AddDate(0, 0, -1)
		startDate = time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, time.UTC)
	}
	endDate = startDate.Add(time.Duration(24*time.Hour - time.Second))
}

// check causes the current program to exit if an error occurred.
func check(e error) {
	if e != nil {
		panic(e)
	}
}

// prepareWorkspace deletes and creates the directory where CloudWatch logs will be processed.
func prepareWorkspace() {
	err := os.RemoveAll(workspace)
	check(err)

	err = os.Mkdir(workspace, 0700)
	check(err)
}

// downloadLogs downloads CloudWatch logs into the workspace.
func downloadLogs(logStream *cloudwatchlogs.LogStream) {
	file, err := os.Create(workspace + string(os.PathSeparator) + *logStream.LogStreamName + ".log")
	check(err)
	defer file.Close()

	writer := bufio.NewWriter(file)
	nextToken := ""
	for {
		logEventInput := &cloudwatchlogs.GetLogEventsInput{
			LogGroupName:  aws.String(environment),
			LogStreamName: logStream.LogStreamName,
			StartTime:     aws.Int64(startDate.UnixNano() / int64(time.Millisecond)),
			EndTime:       aws.Int64(endDate.UnixNano() / int64(time.Millisecond)),
			StartFromHead: aws.Bool(true),
		}
		if len(nextToken) > 0 {
			logEventInput.NextToken = aws.String(nextToken)
		}

		eventList, err := cwService.GetLogEvents(logEventInput)
		check(err)

		for _, eventItem := range eventList.Events {
			writer.WriteString(*eventItem.Message)
			writer.WriteString("\n")
		}

		if len(eventList.Events) > 0 && len(*eventList.NextForwardToken) > 0 {
			nextToken = *eventList.NextForwardToken
		} else {
			break
		}
	}

	writer.Flush()
}

// archiveLogs compressed all downloaded logs into a tar.gz archive.
func archiveLogs(archive *os.File) {
	gw := gzip.NewWriter(archive)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	err := filepath.Walk(workspace, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".log") {
			file, err := os.Open(path)
			check(err)
			defer file.Close()

			header := new(tar.Header)
			header.Name = info.Name()
			header.Size = info.Size()
			header.Mode = int64(info.Mode())
			header.ModTime = info.ModTime()

			// write the header to the tarball archive
			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			// copy the file data to the tarball
			if _, err := io.Copy(tw, file); err != nil {
				return err
			}
		}

		return nil
	})
	check(err)
}

// uploadArchive uploads the generated archive to the S3 bucket.
func uploadArchive(archive *os.File) {
	duration, _ := time.ParseDuration(timeout)

	ctx := context.Background()
	var cancelFn func()
	ctx, cancelFn = context.WithTimeout(ctx, duration)
	defer cancelFn()

	_, err := s3Service.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("/" + environment + "/" + filepath.Base(archive.Name())),
		Body:   io.ReadSeeker(archive),
	})

	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == request.CanceledErrorCode {
			panic(fmt.Errorf("upload canceled due to timeout, %v", err))
		} else {
			panic(fmt.Errorf("failed to upload the archive, %v", err))
		}
	}

	log.Println(fmt.Sprintf("Logs successfully uploaded to \"%s\".", bucket))
}
