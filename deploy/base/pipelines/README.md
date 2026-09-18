# Pipelines et Tasks livrés — Apache 2.0

Ces définitions sont sous **Apache License 2.0** (fichier `LICENSE` de ce
répertoire), et non sous l'AGPL qui couvre le reste d'Arkhitekton.

La raison est pratique : qui écrit ses propres pipelines part presque toujours
de ceux-ci. Les laisser sous AGPL ferait hériter de l'AGPL le pipeline dérivé,
alors même que Tekton ne les lit que comme des données — le contraire de ce que
le contrat des pipelines cherche à permettre (doc 13, doc 17).

La même licence couvre `deploy/base/config/catalog.yaml`, qui sert de modèle de
catalogue.
