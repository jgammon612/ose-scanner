/*
Copyright (C) 2016 Black Duck Software, Inc.
http://www.blackducksoftware.com/

Licensed to the Apache Software Foundation (ASF) under one
or more contributor license agreements. See the NOTICE file
distributed with this work for additional information
regarding copyright ownership. The ASF licenses this file
to you under the Apache License, Version 2.0 (the
"License"); you may not use this file except in compliance
with the License. You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing,
software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
KIND, either express or implied. See the License for the
specific language governing permissions and limitations
under the License.
*/

package controller

import (
	"log"
	"os"
	"sync"

	osclient "github.com/openshift/origin/pkg/client"
	"github.com/openshift/origin/pkg/cmd/util/clientcmd"

	"github.com/spf13/pflag"
	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/meta"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/runtime"
)

type HubParams struct {
	Config  *HubConfig
	Scanner string
	Workers int
	Version string
}

var Hub HubParams

type Controller struct {
	openshiftClient *osclient.Client
	kubeClient      *kclient.Client
	mapper          meta.RESTMapper
	typer           runtime.ObjectTyper
	f               *clientcmd.Factory
	jobQueue        chan Job
	wait            sync.WaitGroup
	images          map[string]*ScanImage
	annotation      *Annotator
	sync.RWMutex
}

func NewController(os *osclient.Client, kc *kclient.Client, hub HubParams) *Controller {

	f := clientcmd.New(pflag.NewFlagSet("empty", pflag.ContinueOnError))
	mapper, typer := f.Object()

	Hub = hub

	jobQueue := make(chan Job, Hub.Workers)

	return &Controller{
		openshiftClient: os,
		kubeClient:      kc,
		mapper:          mapper,
		typer:           typer,
		f:               f,
		jobQueue:        jobQueue,
		images:     make(map[string]*ScanImage),
		annotation: NewAnnotator(os, hub.Version, hub.Config.Host),
	}
}

func (c *Controller) Start() {

	log.Println("Starting controller ....")
	dispatcher := NewDispatcher(c.jobQueue, Hub.Workers)
	dispatcher.Run()

	return
}

func (c *Controller) Watch() {

	log.Println("Starting watcher ....")
	watcher := NewWatcher(c.openshiftClient, c)
	watcher.Run()

	return

}

func (c *Controller) Stop() {

	log.Println("Waiting for scan queue to drain before stopping...")
	c.wait.Wait()

	log.Println("Scan queue empty.")
	log.Println("Controller stopped.")
	return

}

func (c *Controller) Load(done <-chan struct{}) {

	log.Println("Starting load of existing images ...")

	c.getImages(done)

	log.Println("Done load of existing images.")

	return
}

func (c *Controller) AddImage(ID string, Reference string) {

	c.Lock()
	defer c.Unlock()

	_, ok := c.images[Reference]
	if !ok {

		imageItem := newScanImage(ID, Reference, c.annotation)

		if !c.annotation.IsScanNeeded(imageItem.sha) {
			log.Printf("Image sha %s previously scanned. Skipping.\n", imageItem.sha)
			// return
		}

		c.images[Reference] = imageItem

		log.Printf("Added %s to image map\n", imageItem.digest)
		job := Job{
			ScanImage:  imageItem,
			controller: c,
		}

		job.Load()
		c.jobQueue <- job

	}

}

func (c *Controller) getImages(done <-chan struct{}) {

	imageList, err := c.openshiftClient.Images().List(kapi.ListOptions{})

	if err != nil {
		log.Println(err)
		return
	}

	if imageList == nil {
		log.Println("No images")
		return
	}

	for _, image := range imageList.Items {
		c.AddImage(image.DockerImageMetadata.ID, image.DockerImageReference)
	}

	return

}

func (c *Controller) ValidateConfig() bool {

	hubServer := HubServer{Config: Hub.Config}

	return hubServer.login()

}

func (c *Controller) ValidateDockerConfig() bool {
	docker := NewDocker()
	if docker.client == nil {
		log.Printf("Unable to connect to Docker runtime\n")
		return false
	}

	_, err := docker.client.Info()
	if err != nil {
		log.Printf("Unable to connect to Docker runtime. %s\n", err)
		return false
	}

	log.Printf("Validated Docker runtime connection\n")
	return true

}

func init() {
	log.SetFlags(log.LstdFlags)
	log.SetOutput(os.Stdout)
}
