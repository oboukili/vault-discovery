package gce

import (
	"google.golang.org/api/compute/v1"
)

type zonalInstances map[string][]*compute.Instance
