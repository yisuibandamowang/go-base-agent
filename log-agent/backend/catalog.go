package main

type SelectOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type OptionsResponse struct {
	Envs     []SelectOption `json:"envs"`
	Services []SelectOption `json:"services"`
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
	envs := []SelectOption{{Value: "all", Label: "全部环境"}}
	for _, env := range conf.AllowedEnvs {
		envs = append(envs, SelectOption{Value: env, Label: env})
	}
	services := []SelectOption{{Value: "all", Label: "全部服务"}}
	for _, service := range servicesFromConfig(conf) {
		services = append(services, SelectOption{Value: service, Label: service})
	}
	return OptionsResponse{Envs: envs, Services: services}
}

func servicesFromConfig(conf LogReaderConfig) []string {
	if len(conf.Services) > 0 {
		return conf.Services
	}
	return defaultServices()
}
