$env:DOCKER_HOST = "ssh://lifailon@192.168.3.101:2121"
$IMAGE_NAME = "lifailon/logporter"
$IMAGE_TAG = "nightly"
$CONTAINER_NAME = "logporter"
$CONTAINER_ROLLBACK = "${CONTAINER_NAME}-rollback"

docker build -t "${IMAGE_NAME}:${IMAGE_TAG}" .

$CONTAINER_ARGS = $(
    docker inspect --format='
    {{range .Config.Env}}-e "{{.}}" {{end}}
    {{range .HostConfig.Binds}}-v "{{.}}" {{end}}
    {{range $p, $conf := .HostConfig.PortBindings}}{{range $conf}}-p {{.HostPort}}:{{$p}} {{end}}{{end}}
    --network={{range $net, $conf := .NetworkSettings.Networks}}{{$net}}{{end}}
    {{range $k, $v := .Config.Labels}}--label "{{$k}}={{$v}}" {{end}}
    {{if .HostConfig.RestartPolicy.Name}}--restart={{.HostConfig.RestartPolicy.Name}}{{end}}
  ' $CONTAINER_NAME
).Trim() -join " "

try {
    docker stop $CONTAINER_NAME
    docker rename $CONTAINER_NAME $CONTAINER_ROLLBACK
    Invoke-Expression "docker run -d --name $CONTAINER_NAME $CONTAINER_ARGS ${IMAGE_NAME}:${IMAGE_TAG}"
    $CONTAINER_STATE = $(docker inspect --format='{{.State.Running}}' $CONTAINER_NAME)
    if ($CONTAINER_STATE -ne "true") {
        throw "Error starting container"
    }
    docker rm $CONTAINER_ROLLBACK
}
catch {
    docker rm -f $CONTAINER_NAME
    docker start $CONTAINER_NAME
    docker rename $CONTAINER_ROLLBACK $CONTAINER_NAME
}
finally {
    docker logs $CONTAINER_NAME
}