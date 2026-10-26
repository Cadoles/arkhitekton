# Shipped pipelines and tasks — Apache 2.0

These definitions are licensed under the **Apache License 2.0** (the `LICENSE` file in
this directory), not under the AGPL that covers the rest of Arkhitekton.

The reason is practical: people writing their own pipelines almost always start from
these. Leaving them under the AGPL would carry it into the derived pipeline, even though
Tekton only ever reads them as data — the opposite of what the pipeline contract is meant
to allow (see [docs/05](../../../docs/05-pipeline-catalogue.md) and
[docs/12](../../../docs/12-licensing.md)).

The same licence covers `deploy/base/config/catalog.yaml`, which serves as a catalogue
template.
