package main

type SelectOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type OptionsResponse struct {
	Projects    []SelectOption            `json:"projects"`
	Envs        []SelectOption            `json:"envs"`
	Services    []SelectOption            `json:"services"`
	Deployments map[string][]SelectOption `json:"deployments"`
}

func defaultServices() []string {
	return []string{
		"membergateway",
		"pay",
		"webmember",
		"membercenter",
		"memberoperate",
		"membercloudrule",
		"member_data",
		"usermanager",
		"ai",
		"member_cashier",
		"exquisite",
		"recall",
		"cms",
		"strategy_center",
		"membermanager",
		"ordercrontab",
		"template",
	}
}

func optionsResponse(conf LogReaderConfig) OptionsResponse {
	projects := []SelectOption{
		{Value: logProjectMember, Label: "1586 member项目"},
		{Value: logProjectFuyao, Label: "5658 扶摇项目"},
	}
	envs := []SelectOption{{Value: "all", Label: "全部环境"}}
	for _, env := range conf.AllowedEnvs {
		envs = append(envs, SelectOption{Value: env, Label: env})
	}
	services := []SelectOption{{Value: "all", Label: "全部服务"}}
	for _, service := range servicesFromConfig(conf) {
		services = append(services, SelectOption{Value: service, Label: service})
	}
	return OptionsResponse{
		Projects: projects,
		Envs:     envs,
		Services: services,
		Deployments: map[string][]SelectOption{
			logProjectMember: {
				{Value: "all", Label: "全部 Deployment"},
			},
			logProjectFuyao: {
				{Value: "all", Label: "全部 Deployment"},
				{Value: "ad-platform-test", Label: "ad-platform-test"},
				{Value: "ad-platform-regress", Label: "ad-platform-regress"},
				{Value: "ad-platform-online", Label: "ad-platform-online"},
			},
		},
	}
}

func servicesFromConfig(conf LogReaderConfig) []string {
	if len(conf.Services) > 0 {
		return conf.Services
	}
	return defaultServices()
}
