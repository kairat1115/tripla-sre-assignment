package bucket

import "errors"

var (
	errListFailed              = errors.New("list failed")
	errBucketNotFound          = errors.New("bucket not found")
	errReadFailed              = errors.New("read failed")
	errInvalidJSON             = errors.New("invalid JSON")
	errBucketNameMismatch      = errors.New("bucket-name in body must match path parameter")
	errGenerationFailed        = errors.New("generation failed")
	errDeleteFailed            = errors.New("delete failed")
	errMethodNotAllowed        = errors.New("Method Not Allowed")
	errMissingRegion           = errors.New("missing required property: aws-region")
	errMissingACL              = errors.New("missing required property: acl")
	errMissingBucketName       = errors.New("missing required property: bucket-name")
	errInvalidBucketNameLength = errors.New("invalid bucket-name: must be 3-63 characters")
	errInvalidBucketNameChars  = errors.New("invalid bucket-name: must contain only lowercase letters, digits, hyphens, and dots, and start/end with a letter or digit")
	errConsecutiveDots         = errors.New("invalid bucket-name: must not contain consecutive dots")
)
