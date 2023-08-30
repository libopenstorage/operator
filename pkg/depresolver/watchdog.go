package depresolver

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

func (g *Graph) Run() {
	done := make(chan struct{})
	retryQueue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	handler := cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			pod, ok := obj.(*v1.Pod)
			if !ok {
				logrus.Errorf("watch returned non-pod object: %+v", obj)
				return
			}

			if label, ok := pod.Labels["app"]; ok && g.IsResolved(pod, label) {
				err := g.k8sClientset.CoreV1().Pods(pod.Namespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{})
				if nil != err {
					logrus.Errorf("depresolver failed to delete pod %s: %s", pod.Name, err)
					retryQueue.Add(pod)
					return
				}
			} else {
				retryQueue.Add(pod)
			}
		},
	}

	listWatcher := cache.NewListWatchFromClient(
		g.k8sClientset.RESTClient(),
		"pods",
		g.namespace,
		fields.OneTermEqualSelector("metadata.deletionTimestamp", ""),
	)

	informer := cache.NewSharedInformer(
		listWatcher,
		&v1.Pod{},
		time.Second,
	)

	informer.AddEventHandler(handler)

	podKey := func(pod *v1.Pod) string {
		return fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
	}

	queue := cache.NewFIFO(
		func(obj interface{}) (key string, err error) {
			pod, ok := obj.(*v1.Pod)
			if !ok {
				return
			}

			key = podKey(pod)
			return
		})

	podController := cache.New(&cache.Config{
		Queue:         queue,
		ObjectType:    &v1.Pod{},
		RetryOnError:  false,
		ListerWatcher: listWatcher,
		Process: func(obj interface{}) (err error) {
			pod, ok := obj.(*v1.Pod)
			if !ok {
				err = fmt.Errorf("watch returned non-pod object: %+v", obj)
				logrus.Errorln(err)
				return
			}

			if label, ok := pod.Labels["app"]; ok {
				if !g.IsResolved(pod, label) {
					queue.Add(pod)
					return
				}

			}
			err = g.k8sClientset.CoreV1().Pods(pod.Namespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{})
			if nil != err {
				err = fmt.Errorf("depresolver failed to delete pod %s: %s", pod.Name, err)
				logrus.Errorln(err)
				return
			}
			return
		},
		FullResyncPeriod: time.Second,
	})

	go podController.Run(done)

	sigStop := make(chan os.Signal, 1)
	signal.Notify(sigStop, syscall.SIGINT, syscall.SIGTERM)
	<-sigStop
	close(done)

}
