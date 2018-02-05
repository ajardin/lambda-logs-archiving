# Logs archiving with a Lambda function
[![Build Status](https://travis-ci.org/ajardin/lambda-logs-archiving.svg?branch=master)](https://travis-ci.org/ajardin/lambda-logs-archiving)
[![Codacy Badge](https://api.codacy.com/project/badge/Grade/2f71f9519a854a0a9374fd32eef9f02d)](https://www.codacy.com/app/ajardin/lambda-logs-archiving?utm_source=github.com&utm_medium=referral&utm_content=ajardin/lambda-logs-archiving&utm_campaign=badger)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)

## Overview
The idea behind that project is to be able to easily archive logs from CloudWatch into an S3 bucket thanks to AWS features.
It's designed to be used with a scheduled task running everyday in order to retrieve yesterday logs.

## Behavior
1. Retrieve flags value from either the command line or from environment variables.
2. Identify which CloudWatch log streams must be downloaded.
3. Download concurrently all logs with multiple [goroutines](https://gobyexample.com/goroutines).
4. Create a ZIP archive with all these logs.
5. Upload on an S3 bucket.

... That's all!

## Usage
To use Go with a Lambda function, we need a Linux binary that we will compress into a ZIP archive.
```
# Build a binary that will run on Linux
GOOS=linux go build -o logs-archiving logs-archiving.go

# Put the binary into a ZIP archive 
zip logs-archiving.zip logs-archiving
```
Once the archive has been generated, you have to upload it on AWS.

## Configuration
AWS credentials are automatically retrieved from the execution context.
There is no additional configuration required.

Two environment variables must be configured on the Lambda function:
* `BUCKET_NAME`, the S3 bucket name where logs will be archived.
* `ENVIRONMENT_NAME`, the environment name from where logs have been generated.

These values can also be passed manually outside AWS by using:
```
go run logs-archiving.go -bucket XXXXX -environment XXXXX
```

Therefore if you want to use the script locally, you have to replace `lambda.Start(LambdaHandler)` by `LambdaHandler()`. 
The instruction is required by AWS, but it causes an infinite wait when the program is run outside a Lambda context.
