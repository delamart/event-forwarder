package events

forward[{"url":url,"body":body}] {
    url = "https://localhost:9000/post"
    body = json.marshal(input)
}

forward[{"url":url,"body":body}] {
    url = "https://localhost:9000/commit"
    body = json.marshal(input)
}