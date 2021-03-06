# Bazel doesn't have support for adding capabilities to binaries.
# Instead, we create a layer on top of bazel-generated image that
# adds the capabilities as needed.
# For alternative ideas on how to solve this see:
# https://github.com/bazelbuild/rules_docker/issues/752
# NOTE: this process needs explicit CAP_NET_ADMIN from docker.
# E.g. with `cap_add: NET_ADMIN` from docker-compose.
FROM scion_sig_nocap_debug:latest
RUN ["setcap", "cap_net_admin+ei", "/app/sig"]
