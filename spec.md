At CERN we use OpenStack magnum to create new kubernetes clusters. This
provisions a set of OpenStack servers, either via ironic if physic instances
or via nova if virtual machines.

When you create an ingress / gateway, etc.. usually you define a dns name
for that service, for example test.cern.ch. At CERN in order to configure
the DNS servers to create a record pointing to a node, the node must be the
one defining a label.


Here is official docs:

---
landb-alias

Comma separated list of alias names per interface FQDN:

$ openstack server set --property landb-alias="alias1,alias2,alias3" ...
Note
When using multiple interfaces use ";" to separate different interfaces/alias mappings and ":" to map interface fqdn to alias:

$ openstack server set --property landb-alias="INTERFACE_1_FQDN:myalias1,myalias2;INTERFACE_2_FQDN:myalias3,myalias4"
Note
The previous alias list is overwritten by this command. Thus, if you wish to append a new alias, it is necessary to get the value using openstack server show ... and then add the new alias.

Note
An alias will create a CNAME entry in the DNS. If you need an "A" entry instead, you can append --LOAD-1- to your alias name. For example, the alias 'myalias' will create a DNS CNAME entry, an alias called myalias--LOAD-1- will create an "A" entry. The server will be reachable by 'myalias' in both cases.

Alias list longer than 255 characters

If you have a requirement for more than 255 characters to this field, you can use any property keys that start with landb-alias.

# The following commands are equivalent:
$ openstack server set --property landb-alias="alias1,alias2,alias3" ...

$ openstack server set --property landb-alias="alias1" --property landb-alias2="alias2,alias3" ...

---

if we want an alias to point to different nodes, DNS load balancing, then we must:

// Node 1
$ openstack server set --property landb-alias="alias1--load-0-,alias2--load-0-,alias3--load-0-" ...

// Node 2
$ openstack server set --property landb-alias="alias1--load-1-,alias2--load-1-,alias3--load-1-" ...


Also, bery important here is that in openstack when you set a property the property is fully updated,
therefore to remove a single alias we must put all the others but not the want
we want to remove.

On the other side, if what we use in kubernetes is a Load Balancer, automatically
provided by OpenStack, then, to set the dns aliases we must:

Setting domain name for **load balancer**

DNS update time
Please note that the domain name will be made available after 15 minutes in the worst case, waiting for the update of the DNS servers.

Domain name can be set for a load balancer by adding tags. Following command can be used to set domain name:

openstack loadbalancer set --tag landb-alias=my-domain-name mylb

ping my-domain-name.cern.ch
Multiple dns aliases can be specified as multiple tags as shown below:

openstack loadbalancer set --tag "landb-alias=my-domain-one" --tag "landb-alias=my-domain-two" --tag "landb-alias=my-domain-three" mylb
Let's say, if you want to remove my-domain-two, then remove the tag with the domain name as shown below:

openstack loadbalancer unset --tag "landb-alias=my-domain-two" mylb
If you want to remove all dns aliases, then simply remove all landb-alias tags

openstack loadbalancer unset --tag "landb-alias=my-domain-one" --tag "landb-alias=my-domain-three" mylb
