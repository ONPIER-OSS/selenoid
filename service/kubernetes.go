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
	"github.com/google/uuid"
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

	uuid := uuid.New().String()
	name := fmt.Sprintf("browser-%s", uuid)
	log.Printf("[KUBERNETES_BACKEND] new UUID is %s", uuid)
	podClient := clientset.CoreV1().Pods(k.BrowserNamespace)
	env := k.getEnv(k.ServiceBase, k.Caps)

	var statusURL string
	if strings.HasSuffix("/", k.Service.Path) {
		statusURL = k.Service.Path + "status"
	} else {
		statusURL = k.Service.Path + "/status"
	}

	pod := &corev1.Pod{}
	if k.Service.PodTemplate != nil {
		pod = k.Service.PodTemplate.DeepCopy()
	}
	podDefault := k.constructSelenoidRequestPod(name, uuid, env, statusURL)
	if err := mergo.Merge(pod, podDefault); err != nil {
		return nil, err
	}
	pod, err = podClient.Create(context.Background(), pod, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	name = pod.Name
POD_READY:
	for {
		log.Printf("[KUBERNETES_BACKEND] Waiting for the pod to be ready")
		time.Sleep(10 * time.Second)
		pod, err = podClient.Get(context.Background(), pod.Name, metav1.GetOptions{})
		if err != nil {
			log.Print(err)
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				break POD_READY
			}
		}
	}
	log.Printf("[KUBERNETES_BACKEND] Pod is ready")

	svcClient := clientset.CoreV1().Services(k.BrowserNamespace)
	service := k.constructSelenoidService(name, pod, uuid)

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

func (k *Kubernetes) constructSelenoidService(name string, pod *corev1.Pod, reqID string) *corev1.Service {
	return &corev1.Service{
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
				{Name: "browser", Protocol: corev1.ProtocolTCP, Port: 4444},
				{Name: "vnc", Protocol: corev1.ProtocolTCP, Port: 5900},
				{Name: "devtools", Protocol: corev1.ProtocolTCP, Port: 7070},
				{Name: "fileserver", Protocol: corev1.ProtocolTCP, Port: 8080},
				{Name: "clipboard", Protocol: corev1.ProtocolTCP, Port: 9090},
			},
		},
	}
}

func (k *Kubernetes) constructSelenoidRequestPod(name string, reqID string, env []corev1.EnvVar, statusURL string) corev1.Pod {
	return corev1.Pod{
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
						{Name: "browser", Protocol: corev1.ProtocolTCP, ContainerPort: 4444},
						{Name: "vnc", Protocol: corev1.ProtocolTCP, ContainerPort: 5900},
						{Name: "devtools", Protocol: corev1.ProtocolTCP, ContainerPort: 7070},
						{Name: "fileserver", Protocol: corev1.ProtocolTCP, ContainerPort: 8080},
						{Name: "clipboard", Protocol: corev1.ProtocolTCP, ContainerPort: 9090},
					},
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
