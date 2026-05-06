FROM scratch
COPY cyoda-cloud /cyoda-cloud
ENTRYPOINT ["/cyoda-cloud"]
