package docker

import (
	"github.com/hashicorp/terraform/helper/schema"
)

func resourceDockerRegistryImage() *schema.Resource {
	return &schema.Resource{
		Create: resourceDockerRegistryImageCreate,
		Read:   dataSourceDockerRegistryImageRead,
		Update: resourceDockerRegistryImageUpdate,
		Delete: resourceDockerRegistryImageDelete,

		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"sha256_digest": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"push_triggers": {
				Type:     schema.TypeSet,
				Optional: true,
				ForceNew: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Set:      schema.HashString,
			},

			"keep_remote": {
				Type:     schema.TypeBool,
				Optional: true,
			},
		},
	}
}
