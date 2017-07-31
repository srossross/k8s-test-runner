package main

import (
	"flag"
	"fmt"
	"log"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/munnerz/k8s-api-pager-demo/pkg/apis/pager/v1alpha1"
	"github.com/munnerz/k8s-api-pager-demo/pkg/client"
	factory "github.com/munnerz/k8s-api-pager-demo/pkg/informers/externalversions"
)

var (
	// apiserverURL is the URL of the API server to connect to
	apiserverURL = flag.String("apiserver", "http://127.0.0.1:8001", "URL used to access the Kubernetes API server")

	// queue is a queue of resources to be processed. It is the most simple of
	// types of queue and performs no rate limiting.
	queue = workqueue.NewRateLimitingQueue(workqueue.NewItemExponentialFailureRateLimiter(time.Second*5, time.Minute))

	// stopCh can be used to stop all the informer, as well as control loops
	// within the application.
	stopCh = make(chan struct{})

	sharedFactory factory.SharedInformerFactory
)

func main() {
	flag.Parse()

	// create an instance of our own API client
	cl, err := client.NewForConfig(&rest.Config{
		Host: *apiserverURL,
	})

	if err != nil {
		log.Fatalf("error creating api client: %s", err.Error())
	}

	// we use a shared informer from the informer factory, to save calls to the
	// API as we grow our application and so state is consistent between our
	// control loops. We set a resync period of 30 seconds, in case any
	// create/replace/update/delete operations are missed when watching
	sharedFactory = factory.NewSharedInformerFactory(cl, time.Second*30)

	informer := sharedFactory.Pager().V1alpha1().Alerts().Informer()
	// we add a new event handler, watching for changes to API resources.
	informer.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: enqueue,
			UpdateFunc: func(old, cur interface{}) {
				if !reflect.DeepEqual(old, cur) {
					enqueue(cur)
				}
			},
			DeleteFunc: enqueue,
		},
	)

	// start the informer. This will cause it to begin receiving updates from
	// the configured API server and firing event handlers in response.
	sharedFactory.Start(stopCh)

	// wait for the informe rcache to finish performing it's initial sync of
	// resources
	if !cache.WaitForCacheSync(stopCh, informer.HasSynced) {
		log.Fatalf("error waiting for informer cache to sync: %s", err.Error())
	}

	// here we start just one worker reading objects of the queue. If you
	// wanted to parallelize this, you could start many instances of the worker
	// function, then ensure your application handles concurrency correctly.
	work()
}

func sync(al *v1alpha1.Alert) error {
	log.Printf("got alert! %+v", al)
	return nil
}

func work() {
	for {
		// we read a message off the queue
		key, shutdown := queue.Get()

		// if the queue has been shut down, we should exit the work queue here
		if shutdown {
			stopCh <- struct{}{}
			return
		}

		// convert the queue item into a string. If it's not a string, we'll
		// simply discard it as invalid data and log a message.
		var strKey string
		var ok bool
		if strKey, ok = key.(string); !ok {
			runtime.HandleError(fmt.Errorf("key in queue should be of type string but got %T. discarding", key))
			return
		}

		// we define a function here to process a queue item, so that we can
		// use 'defer' to make sure the message is marked as Done on the queue
		func(key string) {
			defer queue.Done(key)

			// attempt to split the 'key' into namespace and object name
			namespace, name, err := cache.SplitMetaNamespaceKey(strKey)

			if err != nil {
				runtime.HandleError(fmt.Errorf("error splitting meta namespace key into parts: %s", err.Error()))
				return
			}

			// retrieve the latest version in the cache of this alert
			obj, err := sharedFactory.Pager().V1alpha1().Alerts().Lister().Alerts(namespace).Get(name)

			if err != nil {
				runtime.HandleError(fmt.Errorf("error getting object '%s/%s' from api: %s", namespace, name, err.Error()))
				return
			}

			// attempt to sync the current state of the world with the desired!
			if err := sync(obj); err != nil {
				runtime.HandleError(fmt.Errorf("error processing item '%s/%s': %s", namespace, name, err.Error()))
				return
			}

			// as we managed to process this successfully, we can forget it
			// from the work queue altogether.
			queue.Forget(key)
		}(strKey)
	}
}

// enqueue will add an object 'obj' into the workqueue. The object being added
// must be of type metav1.Object, metav1.ObjectAccessor or cache.ExplicitKey.
func enqueue(obj interface{}) {
	// DeletionHandlingMetaNamespaceKeyFunc will convert an object into a
	// 'namespace/name' string. We do this because our item may be processed
	// much later than now, and so we want to ensure it gets a fresh copy of
	// the resource when it starts. Also, this allows us to keep adding the
	// same item into the work queue without duplicates building up.
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(fmt.Errorf("error obtaining key for object being enqueue: %s", err.Error()))
		return
	}
	// add the item to the queue
	queue.Add(key)
}
