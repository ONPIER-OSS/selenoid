package service

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"log"

	"dario.cat/mergo"
	"github.com/aerokube/selenoid/session"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type Kubernetes struct {
	ServiceBase
	Environment
	Client *rest.Config
	session.Caps
	BrowserNamespace string
}

func (k *Kubernetes) StartWithCancel() (*StartedService, error) {
	clientset, err := kubernetes.NewForConfig(k.Client)
	if err != nil {
		return nil, err
	}

	name := fmt.Sprintf("selenoid-browser-%d", k.RequestId)
	podClient := clientset.CoreV1().Pods(k.BrowserNamespace)
	reqID := fmt.Sprintf("%d", k.RequestId)
	env := k.getEnv(k.ServiceBase, k.Caps)
	var statusURL string
	if strings.HasSuffix("/", k.Service.Path) {
		statusURL = k.Service.Path + "status"
	} else {
		statusURL = k.Service.Path + "/status"
	}
	pod := &corev1.Pod{}
	if k.Service.PodTemplate != nil {
		pod = k.Service.PodTemplate
	}
	podDefault := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"selenoid-request-id": reqID,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "browser",
					Image: k.Service.Image.(string),
					Env:   env,
					Ports: []corev1.ContainerPort{
						{
							Name:          "browser",
							Protocol:      corev1.ProtocolTCP,
							ContainerPort: 4444,
						},
						{
							Name:          "vnc",
							Protocol:      "TCP",
							ContainerPort: 5900,
						},
						{
							Name:          "devtools",
							Protocol:      "TCP",
							ContainerPort: 7070,
						},
						{
							Name:          "fileserver",
							Protocol:      "TCP",
							ContainerPort: 8080,
						},
						{
							Name:          "clipboard",
							Protocol:      "TCP",
							ContainerPort: 9090,
						},
					},
					//					Args: []string{
					//						"-session-attempt-timeout",
					//						"240s",
					//						"-service-startup-timeout",
					//						"240s",
					//					},
					LivenessProbe: &corev1.Probe{
						InitialDelaySeconds: 20,
						TimeoutSeconds:      10,
						PeriodSeconds:       10,
						FailureThreshold:    20,
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Port: intstr.FromString("browser"),
								Path: statusURL,
							},
						},
					},
					ReadinessProbe: &corev1.Probe{
						InitialDelaySeconds: 20,
						TimeoutSeconds:      10,
						PeriodSeconds:       10,
						FailureThreshold:    20,
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Port: intstr.FromString("browser"),
								Path: statusURL,
							},
						},
					},
				},
			},
		},
	}
	if err := mergo.Merge(pod, podDefault); err != nil {
		return nil, err
	}
	pod, err = podClient.Create(context.Background(), pod, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	starting := true
	for starting {
		log.Printf("[KUBERNETES_BACKEND] Waiting for the pod to be ready")
		time.Sleep(10 * time.Second)
		ready := false
		pod, err = podClient.Get(context.Background(), pod.Name, metav1.GetOptions{})
		if err != nil {
			log.Print(err)
		}
		for _, condition := range pod.Status.Conditions {
			log.Printf("[KUBERNETES_BACKEND] Condition: %s - %s", condition.Type, condition.Status)
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if ready {
			break
		}
	}
	log.Printf("[KUBERNETES_BACKEND] Pod is ready")

	svcClient := clientset.CoreV1().Services(k.BrowserNamespace)
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "Pod",
					UID:        pod.GetUID(),
					Name:       pod.GetName(),
				},
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"selenoid-request-id": reqID,
			},
			Ports: []corev1.ServicePort{
				{
					Name:     "browser",
					Protocol: corev1.ProtocolTCP,
					Port:     4444,
				},
				{
					Name:     "vnc",
					Protocol: "TCP",
					Port:     5900,
				},
				{
					Name:     "devtools",
					Protocol: "TCP",
					Port:     7070,
				},
				{
					Name:     "fileserver",
					Protocol: "TCP",
					Port:     8080,
				},
				{
					Name:     "clipboard",
					Protocol: "TCP",
					Port:     9090,
				},
			},
		},
	}
	_, err = svcClient.Create(context.Background(), service, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	// Wait until pod is running
	podUpdated, err := clientset.CoreV1().Pods(k.BrowserNamespace).Get(context.Background(), name, metav1.GetOptions{})
	svcUpdated, err := clientset.CoreV1().Services(k.BrowserNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	// Create a pod
	// Create a service
	// Wait until pod is ready
	// Define the StartedService
	hp := session.HostPort{
		Selenium: net.JoinHostPort(fmt.Sprintf("%s.%s.svc.cluster.local", name, k.BrowserNamespace), "4444"),
	}
	u := &url.URL{Scheme: "http", Host: hp.Selenium, Path: k.Service.Path}
	s := StartedService{
		Url:    u,
		Origin: net.JoinHostPort(fmt.Sprintf("%s.selenoid.svc.cluster.local", name), "4444"),
		Container: &session.Container{
			ID:        string(podUpdated.ObjectMeta.GetUID()),
			IPAddress: svcUpdated.Spec.ClusterIP,
			Ports:     map[string]string{"4444": "4444"},
		},
		HostPort: hp,
		Cancel: func() {
			if err := k.Cancel(context.Background(), k.RequestId, podUpdated.Name, svcUpdated.Name); err != nil {
				log.Printf("[KUBERNETES_ERROR] %s", err)
			}
		},
	}
	return &s, nil
}

func (k *Kubernetes) Cancel(ctx context.Context, requestID uint64, podName, serviceName string) error {

	clientset, err := kubernetes.NewForConfig(k.Client)
	if err != nil {
		return err
	}
	podClient := clientset.CoreV1().Pods(k.BrowserNamespace)
	if err := podClient.Delete(ctx, podName, *metav1.NewDeleteOptions(60)); err != nil {
		return err
	}
	svcClient := clientset.CoreV1().Services(k.BrowserNamespace)
	if err := svcClient.Delete(ctx, serviceName, *metav1.NewDeleteOptions(60)); err != nil {
		return err
	}
	return nil
}

func (k *Kubernetes) getEnv(service ServiceBase, caps session.Caps) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{
			Name:  "TZ",
			Value: getTimeZone(service, caps).String(),
		},
		{
			Name:  "SCREEN_RESOLUTION",
			Value: caps.ScreenResolution,
		},
		{
			Name:  "ENABLE_VNC",
			Value: strconv.FormatBool(caps.VNC),
		},
		{
			Name:  "ENABLE_VIDEO",
			Value: strconv.FormatBool(caps.Video),
		},
	}
	if caps.Skin != "" {
		env = append(env, corev1.EnvVar{Name: "SKIN", Value: caps.Skin})
	}
	if caps.VideoCodec != "" {
		env = append(env, corev1.EnvVar{Name: "CODEC", Value: caps.VideoCodec})
	}

	for _, serviceEnv := range service.Service.Env {
		name, value, _ := strings.Cut(serviceEnv, "=")
		env = append(env, corev1.EnvVar{Name: name, Value: value})
	}

	for _, capsEnv := range caps.Env {
		name, value, _ := strings.Cut(capsEnv, "=")
		env = append(env, corev1.EnvVar{Name: name, Value: value})
	}
	return env
}
