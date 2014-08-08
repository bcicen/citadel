package citadel

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/samalba/dockerclient"
)

type Engine struct {
	ID     string   `json:"id,omitempty"`
	Addr   string   `json:"addr,omitempty"`
	Cpus   float64  `json:"cpus,omitempty"`
	Memory float64  `json:"memory,omitempty"`
	Labels []string `json:"labels,omitempty"`

	client       *dockerclient.DockerClient
	eventHandler EventHandler
}

func (e *Engine) Connect(config *tls.Config) error {
	c, err := dockerclient.NewDockerClient(e.Addr, config)
	if err != nil {
		return err
	}

	e.client = c

	return nil
}

func (e *Engine) SetClient(c *dockerclient.DockerClient) {
	e.client = c
}

// IsConnected returns true if the engine is connected to a remote docker API
func (e *Engine) IsConnected() bool {
	return e.client != nil
}

func (e *Engine) Start(c *Container) error {
	var (
		err    error
		env    = []string{}
		client = e.client
		i      = c.Image
	)
	c.Engine = e

	for k, v := range i.Environment {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	env = append(env,
		fmt.Sprintf("_citadel_type=%s", i.Type),
		fmt.Sprintf("_citadel_labels=%s", strings.Join(i.Labels, ",")),
	)

	config := &dockerclient.ContainerConfig{
		Hostname:     i.Hostname,
		Domainname:   i.Domainname,
		Image:        i.Name,
		Cmd:          i.Args,
		Memory:       int(i.Memory) * 1024 * 1024,
		Env:          env,
		CpuShares:    int(i.Cpus * 100.0 / e.Cpus),
		ExposedPorts: make(map[string]struct{}),
	}

	hostConfig := &dockerclient.HostConfig{
		PublishAllPorts: len(i.BindPorts) == 0,
		PortBindings:    make(map[string][]dockerclient.PortBinding),
	}

	for _, b := range i.BindPorts {
		key := fmt.Sprintf("%d/%s", b.Port, b.Proto)
		config.ExposedPorts[key] = struct{}{}

		hostConfig.PortBindings[key] = []dockerclient.PortBinding{
			{
				HostPort: fmt.Sprint(b.Port),
			},
		}
	}

retry:
	if c.ID, err = client.CreateContainer(config, c.Name); err != nil {
		if err != dockerclient.ErrNotFound {
			return err
		}

		if err := client.PullImage(i.Name, "latest"); err != nil {
			return err
		}

		goto retry
	}

	if err := client.StartContainer(c.ID, hostConfig); err != nil {
		return err
	}

	return e.updatePortInformation(c)
}

func (e *Engine) ListImages() ([]string, error) {
	images, err := e.client.ListImages()
	if err != nil {
		return nil, err
	}

	out := []string{}

	for _, i := range images {
		for _, t := range i.RepoTags {
			out = append(out, t)
		}
	}

	return out, nil
}

func (e *Engine) updatePortInformation(c *Container) error {
	info, err := e.client.InspectContainer(c.ID)
	if err != nil {
		return err
	}

	return parsePortInformation(info, c)
}

func (e *Engine) ListContainers() ([]*Container, error) {
	out := []*Container{}

	c, err := e.client.ListContainers(false)
	if err != nil {
		return nil, err
	}

	for _, ci := range c {
		cc, err := FromDockerContainer(ci.Id, ci.Image, e)
		if err != nil {
			return nil, err
		}

		out = append(out, cc)
	}

	return out, nil
}

func (e *Engine) Kill(container *Container, sig int) error {
	return e.client.KillContainer(container.ID)
}

func (e *Engine) Stop(container *Container) error {
	return e.client.StopContainer(container.ID, 8)
}

func (e *Engine) Remove(container *Container) error {
	return e.client.RemoveContainer(container.ID)
}

func (e *Engine) Events(h EventHandler) error {
	if e.eventHandler != nil {
		return fmt.Errorf("event handler already set")
	}
	e.eventHandler = h

	e.client.StartMonitorEvents(e.handler)

	return nil
}

func (e *Engine) String() string {
	return fmt.Sprintf("engine %s addr %s", e.ID, e.Addr)
}

func (e *Engine) handler(ev *dockerclient.Event, args ...interface{}) {
	event := &Event{
		Engine: e,
		Type:   ev.Status,
		Time:   time.Unix(int64(ev.Time), 0),
	}

	container, err := FromDockerContainer(ev.Id, ev.From, e)
	if err != nil {
		// TODO: un fuck this shit, fuckin handler
		return
	}

	event.Container = container

	e.eventHandler.Handle(event)
}
