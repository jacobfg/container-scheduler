package scheduler

import (
	"context"
	"fmt"
	"github.com/moby/moby/client"
)

type Docker struct{ Client *client.Client }

func (d Docker) Labels(ctx context.Context, selector string) ([]map[string]string, error) {
	result, err := d.Client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", selector)})
	if err != nil {
		return nil, err
	}
	labels := make([]map[string]string, 0, len(result.Items))
	for _, item := range result.Items {
		labels = append(labels, item.Labels)
	}
	return labels, nil
}
func (d Docker) Running(ctx context.Context, name string) (bool, error) {
	result, err := d.Client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return false, err
	}
	if result.Container.State == nil {
		return false, fmt.Errorf("missing container state")
	}
	return result.Container.State.Running || result.Container.State.Restarting || result.Container.State.Paused, nil
}
func (d Docker) Start(ctx context.Context, name string) error {
	_, err := d.Client.ContainerStart(ctx, name, client.ContainerStartOptions{})
	return err
}
