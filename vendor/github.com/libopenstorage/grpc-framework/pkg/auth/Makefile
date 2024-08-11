SUBDIRS = role ownership
PROTODIRS = $(SUBDIRS:%=proto-%)

all: $(SUBDIRS)

proto: $(PROTODIRS)

$(SUBDIRS):
	$(MAKE) -C $@

$(PROTODIRS):
	$(MAKE) -C $(@:proto-%=%) proto