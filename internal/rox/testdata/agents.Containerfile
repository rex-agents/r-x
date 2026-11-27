FROM rox:web
USER root
COPY bootstrap.sh /usr/local/share/rox/bootstrap.sh
USER workspace
RUN ["env","HOME=/home/workspace","bash","/usr/local/share/rox/bootstrap.sh","agents","claude=2.1.0"]
USER root
