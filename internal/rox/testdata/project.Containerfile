FROM rox:linux
USER root
RUN install -d -m 700 -o workspace -g workspace /home/workspace/.rox
COPY bootstrap.sh /usr/local/share/rox/bootstrap.sh
COPY --chown=workspace:workspace project /home/workspace/web
USER workspace
RUN ["bash","/usr/local/share/rox/bootstrap.sh","bake","/home/workspace/web","container","rox:web"]
USER root
