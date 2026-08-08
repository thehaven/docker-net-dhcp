## ADDED Requirements

### Requirement: Legacy and current driver identities remain recognisable

Compatibility and cache-reconciliation logic SHALL recognise both `ghcr.io/thehaven/docker-net-dhcp:<tag>` and `ghcr.io/devplayer0/docker-net-dhcp:<tag>` as this plugin family. Recognition SHALL not imply automatic migration or enablement.

#### Scenario: Current driver is encountered
- **WHEN** cache reconciliation sees a The Haven driver reference
- **THEN** it recognises the network as plugin-managed

#### Scenario: Legacy driver is encountered
- **WHEN** cache reconciliation sees a devplayer0 driver reference
- **THEN** it recognises the network as plugin-managed and does not ghost-prune it

#### Scenario: Unrelated driver is encountered
- **WHEN** cache reconciliation sees another repository or driver
- **THEN** it does not classify it as this plugin family

### Requirement: Migration inspection is read-only

The project SHALL provide a documented or executable read-only inspection path that reports network name, actual driver, intended driver, endpoint/container count, and migration risk. It SHALL not delete, recreate, rename, or attach/detach networks or containers.

#### Scenario: Production legacy network is inspected
- **WHEN** inspection finds `vlan107` using `ghcr.io/devplayer0/docker-net-dhcp:golang`
- **THEN** it reports the mismatch and active endpoint count without mutating Docker state

#### Scenario: Current empty network is inspected
- **WHEN** inspection finds `thehaven-vlan107` using `ghcr.io/thehaven/docker-net-dhcp:golang`
- **THEN** it reports the current driver and empty/active endpoint state without mutation

### Requirement: Deployment documentation separates migration

Normal deployment documentation SHALL instruct operators to use the The Haven plugin identity and SHALL explicitly state that changing an existing network driver is a separate controlled maintenance operation.

#### Scenario: Operator follows normal deployment instructions
- **WHEN** an operator follows the documented build/install path
- **THEN** it does not receive instructions to enable the legacy plugin or recreate `vlan107`

#### Scenario: Operator plans migration
- **WHEN** an operator needs to move `vlan107` to the current driver
- **THEN** documentation requires inventory, maintenance approval, rollback planning, and endpoint/container verification before mutation
