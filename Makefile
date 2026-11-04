# Thin wrappers over dc.py, for people who prefer make. dc.py does the work, so
# nothing here is needed to run the project. Run "make help" for the list.
#
# PYTHON defaults to python on Windows and python3 elsewhere. Override it with
# for example: make PYTHON=python3.12 setup

ifeq ($(OS),Windows_NT)
PYTHON ?= python
else
PYTHON ?= python3
endif

.PHONY: help setup backend frontend dev demo test build stop clean doctor

help:
	@$(PYTHON) dc.py --help

setup:
	$(PYTHON) dc.py setup

backend:
	$(PYTHON) dc.py backend

frontend:
	$(PYTHON) dc.py frontend

dev:
	$(PYTHON) dc.py dev

demo:
	$(PYTHON) dc.py demo

test:
	$(PYTHON) dc.py test

build:
	$(PYTHON) dc.py build

stop:
	$(PYTHON) dc.py stop

clean:
	$(PYTHON) dc.py clean

doctor:
	$(PYTHON) dc.py doctor
