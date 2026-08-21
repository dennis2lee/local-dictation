"""Local Dictation server.

Two identical processes run this package, differing only in the language they
are pinned to and the port they listen on. Nothing in here inspects audio to
guess a language: `settings.model.language` is authoritative.
"""

__version__ = "0.1.10"
PROTOCOL_VERSION = 1
