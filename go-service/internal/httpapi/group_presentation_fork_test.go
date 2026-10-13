package httpapi

type timelineViewResponse struct {
	Groups            []presentationTimelineGroup `json:"groups"`
	WorldlineTopology worldlineTopologyViewModel  `json:"worldline_topology"`
}
