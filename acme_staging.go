package main

import "golang.org/x/crypto/acme"

func stagingClient() *acme.Client {
	return &acme.Client{DirectoryURL: "https://acme-staging-v02.api.letsencrypt.org/directory"}
}
