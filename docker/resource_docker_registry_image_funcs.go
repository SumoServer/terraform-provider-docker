package docker

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"encoding/base64"
	"encoding/json"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/hashicorp/terraform/helper/schema"
)

func resourceDockerRegistryImageCreate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ProviderConfig).DockerClient

	imageName := d.Get("name").(string)
	err := pushImage(client, meta.(*ProviderConfig).AuthConfigs, imageName)
	if err != nil {
		return fmt.Errorf("Unable to push Docker image: %s", err)
	}

	return dataSourceDockerRegistryImageRead(d, meta)
}

func resourceDockerRegistryImageUpdate(d *schema.ResourceData, meta interface{}) error {
	// Update only exists to enable keep_remote to be toggled.
	return dataSourceDockerRegistryImageRead(d, meta)
}

func resourceDockerRegistryImageDelete(d *schema.ResourceData, meta interface{}) error {
	if keepRemote := d.Get("keep_remote").(bool); keepRemote {
		return nil
	}

	pullOpts := parseImageOptions(d.Get("name").(string))
	digest := d.Get("sha256_digest").(string)
	authConfig := meta.(*ProviderConfig).AuthConfigs

	// Use the official Docker Hub if a registry isn't specified
	if pullOpts.Registry == "" {
		pullOpts.Registry = "registry.hub.docker.com"
	} else {
		// Otherwise, filter the registry name out of the repo name
		pullOpts.Repository = strings.Replace(pullOpts.Repository, pullOpts.Registry+"/", "", 1)
	}

	if pullOpts.Registry == "registry.hub.docker.com" {
		// Docker prefixes 'library' to official images in the path; 'consul' becomes 'library/consul'
		if !strings.Contains(pullOpts.Repository, "/") {
			pullOpts.Repository = "library/" + pullOpts.Repository
		}
	}

	if pullOpts.Tag == "" {
		pullOpts.Tag = "latest"
	}

	username := ""
	password := ""

	if auth, ok := authConfig.Configs[normalizeRegistryAddress(pullOpts.Registry)]; ok {
		username = auth.Username
		password = auth.Password
	}

	err := removeRegistryImage(pullOpts.Registry, pullOpts.Repository, digest, username, password)
	if err != nil {
		return fmt.Errorf("Unable to remove Docker image: %s", err)
	}

	d.SetId("")
	return nil
}

func pushImage(client *client.Client, authConfig *AuthConfigs, image string) error {
	pullOpts := parseImageOptions(image)

	// If a registry was specified in the image name, try to find auth for it
	auth := types.AuthConfig{}
	if pullOpts.Registry != "" {
		if authConfig, ok := authConfig.Configs[normalizeRegistryAddress(pullOpts.Registry)]; ok {
			auth = authConfig
		}
	} else {
		// Try to find an auth config for the public docker hub if a registry wasn't given
		if authConfig, ok := authConfig.Configs["https://registry.hub.docker.com"]; ok {
			auth = authConfig
		}
	}

	encodedJSON, err := json.Marshal(auth)
	if err != nil {
		return fmt.Errorf("error creating auth config: %s", err)
	}

	out, err := client.ImagePush(context.Background(), image, types.ImagePushOptions{
		RegistryAuth: base64.URLEncoding.EncodeToString(encodedJSON),
	})
	if err != nil {
		return fmt.Errorf("error pulling image %s: %s", image, err)
	}
	defer out.Close()

	return processStreamingOutput(out)
}

func removeRegistryImage(registry, image, manifest, username, password string) error {
	client := http.DefaultClient

	// Allow insecure registries only for ACC tests
	// cuz we don't have a valid certs for this case
	if env, okEnv := os.LookupEnv("TF_ACC"); okEnv {
		if i, errConv := strconv.Atoi(env); errConv == nil && i >= 1 {
			cfg := &tls.Config{
				InsecureSkipVerify: true,
			}
			client.Transport = &http.Transport{
				TLSClientConfig: cfg,
			}
		}
	}

	req, err := http.NewRequest("DELETE", "https://"+registry+"/v2/"+image+"/manifests/"+manifest, nil)
	if err != nil {
		return fmt.Errorf("Error creating registry request: %s", err)
	}

	if username != "" {
		req.SetBasicAuth(username, password)
	}

	resp, err := client.Do(req)

	if err != nil {
		return fmt.Errorf("Error during registry request: %s", err)
	}

	switch resp.StatusCode {
	// Basic auth was valid or not needed
	case http.StatusAccepted:
		return nil

	// Assume the manifest was deleted
	case http.StatusNotFound:
		return nil

	// Either OAuth is required or the basic auth creds were invalid
	case http.StatusUnauthorized:
		if strings.HasPrefix(resp.Header.Get("www-authenticate"), "Bearer") {
			auth := parseAuthHeader(resp.Header.Get("www-authenticate"))
			params := url.Values{}
			params.Set("service", auth["service"])
			params.Set("scope", auth["scope"])
			tokenRequest, err := http.NewRequest("GET", auth["realm"]+"?"+params.Encode(), nil)

			if err != nil {
				return fmt.Errorf("Error creating registry request: %s", err)
			}

			if username != "" {
				tokenRequest.SetBasicAuth(username, password)
			}

			tokenResponse, err := client.Do(tokenRequest)

			if err != nil {
				return fmt.Errorf("Error during registry request: %s", err)
			}

			if tokenResponse.StatusCode != http.StatusOK {
				return fmt.Errorf("Got bad response from registry: " + tokenResponse.Status)
			}

			body, err := ioutil.ReadAll(tokenResponse.Body)
			if err != nil {
				return fmt.Errorf("Error reading response body: %s", err)
			}

			token := &TokenResponse{}
			err = json.Unmarshal(body, token)
			if err != nil {
				return fmt.Errorf("Error parsing OAuth token response: %s", err)
			}

			req.Header.Set("Authorization", "Bearer "+token.Token)
			digestResponse, err := client.Do(req)

			if err != nil {
				return fmt.Errorf("Error during registry request: %s", err)
			}

			if digestResponse.StatusCode != http.StatusOK {
				return fmt.Errorf("Got bad response from registry: " + digestResponse.Status)
			}

			return nil
		}

		return fmt.Errorf("Bad credentials: " + resp.Status)

		// Some unexpected status was given, return an error
	default:
		return fmt.Errorf("Got bad response from registry: " + resp.Status)
	}
}
